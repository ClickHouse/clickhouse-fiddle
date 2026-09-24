package dockerengine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sync"
	"time"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/cibuild"
	"github.com/lodthe/clickhouse-playground/internal/dbsettings"
	"github.com/lodthe/clickhouse-playground/internal/dbsettings/runsettings"
	"github.com/lodthe/clickhouse-playground/internal/dockertag"
	"github.com/lodthe/clickhouse-playground/internal/metrics"
	"github.com/lodthe/clickhouse-playground/internal/qrunner"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"
	"github.com/lodthe/clickhouse-playground/pkg/chspec"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	dockercli "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

type ImageStorage interface {
	Find(version string) (dockertag.Image, bool)
}

// Runner is a runner that creates database instances using Docker Engine API.
//
// This runner can start instances on arbitrary type of server, even on the same server where the coordinator
// is started. The main requirement is the running Docker daemon and granted access to it.
type Runner struct {
	ctx    context.Context
	cancel context.CancelFunc

	logger zerolog.Logger

	name string
	cfg  Config

	engine       *engineProvider
	tagStorage   ImageStorage
	builder      *buildManager
	pipelineMetr *metrics.PipelineExporter

	workers   sync.WaitGroup
	gc        *garbageCollector
	status    *statusCollector
	prewarmer *prewarmer
}

// New creates a Docker Engine runner. resolver may be nil when local builds are disabled;
// in that case only release (Docker Hub) builds are supported.
func New(ctx context.Context, logger zerolog.Logger, name string, cfg Config, tagStorage ImageStorage, resolver cibuild.Resolver) (*Runner, error) {
	engine, err := newProvider(ctx, cfg.DaemonURL)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Docker engine provider")
	}

	ctx, cancel := context.WithCancel(ctx)

	logger = logger.With().Str("runner", name).Logger()

	runner := &Runner{
		ctx:          ctx,
		cancel:       cancel,
		logger:       logger,
		name:         name,
		cfg:          cfg,
		engine:       engine,
		tagStorage:   tagStorage,
		builder:      newBuildManager(ctx, logger, engine, resolver, cfg.Builds),
		pipelineMetr: metrics.NewPipelineExporter(string(qrunner.TypeDockerEngine), name),
	}

	runner.gc = newGarbageCollector(ctx, logger, cfg.GC, engine, metrics.NewRunnerGCExporter(string(qrunner.TypeDockerEngine), name))
	runner.status = newStatusCollector(ctx, logger, cfg.StatusCollectionFrequency, engine, metrics.NewRunnerStatusExporter(string(qrunner.TypeDockerEngine), name))
	runner.prewarmer = newPrewarmer(ctx, logger, runner, runner.engine, cfg.MaxWarmContainers)

	return runner, nil
}

func (r *Runner) Type() qrunner.Type {
	return qrunner.TypeDockerEngine
}

func (r *Runner) Name() string {
	return r.name
}

func (r *Runner) Status(ctx context.Context) qrunner.RunnerStatus {
	err := r.engine.ping(ctx)

	return qrunner.RunnerStatus{
		Alive:            err == nil,
		LivenessProbeErr: err,
	}
}

// Start runs the following background tasks:
// 1) gc -- prunes containers and images;
// 2) status exporter -- exports information about current state of the runner.
func (r *Runner) Start() error {
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		r.gc.start()
	}()

	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		r.status.start()
	}()

	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		_ = r.prewarmer.Start()
	}()

	logCtx := r.logger.Info()
	if r.cfg.DaemonURL != nil {
		logCtx = logCtx.Str("daemon_url", *r.cfg.DaemonURL)
	}
	logCtx.Msg("runner has been started")

	return nil
}

func (r *Runner) Stop(shutdownCtx context.Context) error {
	r.logger.Info().Msg("stopping")

	r.prewarmer.Stop(shutdownCtx)

	r.cancel()
	r.workers.Wait()

	r.logger.Info().Msg("runner has been stopped")

	return nil
}

func (r *Runner) RunQuery(ctx context.Context, run *queryrun.Run) (output string, err error) {
	bt, err := buildtype.Parse(run.BuildType)
	if err != nil {
		return "", err
	}

	state := &requestState{
		runID:     run.ID,
		database:  run.Database,
		version:   run.Version,
		buildType: bt,
		query:     run.Input,
		settings:  run.Settings,
	}

	state.imageTag, state.imageFQN, err = r.resolveImage(ctx, state.version, state.buildType)
	if err != nil {
		return "", fmt.Errorf("failed to construct FQN: %w", err)
	}

	containerID, found, err := r.prewarmer.Fetch(state.imageFQN)
	if err != nil {
		r.logger.Err(err).Str("run_id", state.runID).Msg("failed to fetch a prewarmed container")
	}
	if found {
		state.containerID = containerID
	} else {
		err := r.createContainer(ctx, state)
		if err != nil {
			return "", fmt.Errorf("failed to create container: %w", err)
		}
	}

	r.prewarmer.PushNewRequest(*state)

	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}

		startedAt := time.Now()
		defer func() {
			r.pipelineMetr.RemoveContainer(err == nil, "", startedAt)
		}()

		err := r.engine.removeContainer(r.ctx, state.containerID)
		if err != nil {
			r.logger.Error().Err(err).Str("run_id", state.runID).Msg("failed to kill container")
			return
		}

		r.logger.Debug().Str("container_id", state.containerID).Msg("container has been force removed")
	}()

	output, err = r.runQueryWithContainer(ctx, state)
	if err != nil {
		return "", errors.Wrap(err, "failed to run query")
	}

	return output, nil
}

// resolveImage builds the image tag and FQN for a (version, build type) pair.
//
// For release builds the image comes from Docker Hub:
//   - image tag: image name in format <repository>:<version>
//   - image FQN: a unique fully qualified name that includes the exact version of the image
//
// For non-release builds the image is built locally; the tag is empty and the FQN is a
// content-addressed name derived from the resolved CI artifacts.
func (r *Runner) resolveImage(ctx context.Context, version string, bt buildtype.BuildType) (imageTag string, imageFQN string, err error) {
	if !bt.IsRelease() {
		if !r.builder.enabled() {
			return "", "", qrunner.ErrBuildsNotSupported
		}

		fqn, _, err := r.builder.resolve(ctx, version, bt)
		if err != nil {
			return "", "", err
		}

		return "", fqn, nil
	}

	img, found := r.tagStorage.Find(version)
	if !found {
		return "", "", errors.New("version not found")
	}

	imageTag = FullImageName(img.Repository, version)

	// When the digest cannot be resolved, fall back to tag-addressed naming so the query
	// can still run. An image cached under the tag-addressed name may be stale for mutable
	// tags (head, latest) until the digest resolves again.
	discriminator := img.Digest
	if discriminator == "" {
		discriminator = img.Tag
	}
	imageFQN = PlaygroundImageName(img.Repository, discriminator)

	return imageTag, imageFQN, nil
}

// createContainer makes sure the image is present and runs a container with a database.
// Release images are pulled on demand; non-release images must have been built beforehand
// via EnsureImage (the prepare flow), otherwise ErrImageNotReady is returned.
func (r *Runner) createContainer(ctx context.Context, state *requestState) error {
	if state.imageFQN == "" {
		var err error
		state.imageTag, state.imageFQN, err = r.resolveImage(ctx, state.version, state.buildType)
		if err != nil {
			return fmt.Errorf("failed to construct FQN: %w", err)
		}
	}

	if state.buildType.IsRelease() {
		err := r.pull(ctx, state)
		if err != nil {
			return fmt.Errorf("pull failed: %w", err)
		}
	} else if !r.checkIfImageExists(ctx, state) {
		return qrunner.ErrImageNotReady
	}

	err := r.runContainer(ctx, state)
	if err != nil {
		return fmt.Errorf("container run failed: %w", err)
	}

	return err
}

// EnsureImage makes sure the image for the given version and build type is built and cached.
func (r *Runner) EnsureImage(ctx context.Context, version string, bt buildtype.BuildType) (qrunner.ImageStatus, error) {
	if bt.IsRelease() {
		// Release images are pulled on demand inside RunQuery.
		return qrunner.ImageStatus{State: qrunner.ImageReady}, nil
	}

	return r.builder.Ensure(ctx, version, bt)
}

// pull checks whether the requested image exists. If no, it will be downloaded and renamed to hashed-name.
func (r *Runner) pull(ctx context.Context, state *requestState) (err error) {
	startedAt := time.Now()

	if r.checkIfImageExists(ctx, state) {
		return nil
	}

	out, err := r.engine.pullImage(ctx, state.imageTag)
	if err != nil {
		r.pipelineMetr.PullNewImage(false, state.version, startedAt)
		return errors.Wrap(err, "docker pull failed")
	}

	// We should read the output to be sure that the image has been pulled.
	_, err = io.ReadAll(out)
	if err != nil {
		r.logger.Error().Err(err).Str("image", state.imageTag).Msg("failed to read pull output")
	}

	r.logger.Debug().Str("image", state.imageTag).Msg("base image has been pulled")

	err = r.engine.addImageTag(ctx, state.imageTag, state.imageFQN)
	if err != nil {
		r.pipelineMetr.PullNewImage(false, state.version, startedAt)
		r.logger.Error().Err(err).
			Str("run_id", state.runID).
			Str("source", state.imageTag).
			Str("target", state.imageFQN).
			Msg("failed to rename image")

		return errors.Wrap(err, "failed to tag image")
	}

	r.pipelineMetr.PullNewImage(true, state.version, startedAt)
	r.logger.Debug().
		Str("run_id", state.runID).
		Dur("elapsed_ms", time.Since(startedAt)).
		Str("image", state.imageTag).
		Msg("image has been pulled")

	return nil
}

func (r *Runner) checkIfImageExists(ctx context.Context, state *requestState) bool {
	startedAt := time.Now()

	_, err := r.engine.getImageByID(ctx, state.imageFQN)
	if err == nil {
		r.pipelineMetr.PullExistedImage(true, state.version, startedAt)
		r.logger.Debug().
			Dur("elapsed_ms", time.Since(startedAt)).
			Str("image", state.imageFQN).
			Msg("image has already been pulled")

		return true
	}
	if err != nil && !dockercli.IsErrNotFound(err) {
		r.pipelineMetr.PullExistedImage(false, state.version, startedAt)
		r.logger.Error().Err(err).Str("image", state.imageFQN).Msg("docker inspect failed")
	}

	return false
}

// keeperConfigSupported reports whether the in-process Keeper config may be mounted:
// any non-release build (they are all built from modern sources), or releases since 22.3,
// where ClickHouse Keeper was declared production-ready. Older releases keep the clear
// 'ZooKeeper is not configured' error instead of connection-timeout noise.
func keeperConfigSupported(bt buildtype.BuildType, version string) bool {
	return !bt.IsRelease() || chspec.IsAtLeastMajor(version, "22.3")
}

// runContainer starts a container and returns its id.
func (r *Runner) runContainer(ctx context.Context, state *requestState) (err error) {
	invokedAt := time.Now()
	defer func() {
		r.pipelineMetr.CreateContainer(err == nil, state.version, invokedAt)
	}()

	contConfig := &container.Config{
		Image:  state.imageFQN,
		Labels: CreateContainerLabels(r.name, state.runID, state.version),
	}

	var networkMode string
	if r.cfg.Container.NetworkMode != nil {
		networkMode = *r.cfg.Container.NetworkMode
	}

	// Network is disabled to prevent malicious attacks and to optimize container start up.
	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode(networkMode),
		Resources: container.Resources{
			NanoCPUs:   int64(r.cfg.Container.CPULimit),
			CpusetCpus: r.cfg.Container.CPUSet,
			Memory:     int64(r.cfg.Container.MemoryLimit),
		},
	}

	// A custom config is used to disable some ClickHouse features to speed up the startup.
	if r.cfg.CustomConfigPath != nil {
		hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   *r.cfg.CustomConfigPath,
			Target:   fmt.Sprintf("/etc/clickhouse-server/config.d/custom-config%s", path.Ext(*r.cfg.CustomConfigPath)),
			ReadOnly: true,
		})
	}

	// The in-process Keeper is enabled so coordination-dependent features can be tested.
	if r.cfg.KeeperConfigPath != nil && keeperConfigSupported(state.buildType, state.version) {
		hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   *r.cfg.KeeperConfigPath,
			Target:   fmt.Sprintf("/etc/clickhouse-server/config.d/keeper-config%s", path.Ext(*r.cfg.KeeperConfigPath)),
			ReadOnly: true,
		})
	}

	// A custom quotas config is used to avoid DOS.
	if r.cfg.QuotasPath != nil {
		hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   *r.cfg.QuotasPath,
			Target:   fmt.Sprintf("/etc/clickhouse-server/users.d/default%s", path.Ext(*r.cfg.QuotasPath)),
			ReadOnly: true,
		})
	}

	cont, err := r.engine.createContainer(ctx, contConfig, hostConfig)
	if err != nil {
		return errors.Wrap(err, "container cannot be created")
	}

	createdAt := time.Now()

	debugInfo := map[string]any{
		"run_id":       state.runID,
		"image":        state.imageFQN,
		"container_id": cont.ID,
	}
	r.logger.Debug().
		Fields(debugInfo).
		Dur("elapsed_ms", time.Since(invokedAt)).
		Msg("container has been created")

	err = r.engine.startContainer(ctx, cont.ID)
	if err != nil {
		return errors.Wrap(err, "container cannot be started")
	}

	r.logger.Debug().
		Fields(debugInfo).
		Dur("elapsed_ms", time.Since(createdAt)).
		Msg("container has been started")

	state.containerID = cont.ID

	return nil
}

func (r *Runner) execQuery(ctx context.Context, state *requestState) (stdout string, stderr string, err error) {
	var args []string

	switch state.settings.Type() {
	case dbsettings.TypeClickHouse:
		args = []string{
			"clickhouse", "client",
			"-n",
			"-m",
			"--query", state.query,
		}

		settings, ok := state.settings.(*runsettings.ClickHouseSettings)
		if !ok {
			return "", "", errors.Errorf("invalid settings for type %s", state.settings.Type())
		}

		formatArgs := settings.FormatArgs(state.version, r.cfg.DefaultOutputFormat)
		args = append(args, formatArgs...)
	default:
		return "", "", errors.Errorf("unknown settings type %s", state.settings.Type())
	}

	stdout, stderr, _, err = r.execCommand(ctx, state, args)

	return stdout, stderr, err
}

func (r *Runner) runQueryWithContainer(ctx context.Context, state *requestState) (output string, err error) {
	invokedAt := time.Now()
	defer func() {
		r.pipelineMetr.RunQuery(err == nil, state.version, invokedAt)
	}()

	ready, output, err := r.waitUntilReady(ctx, state)
	if err != nil {
		return "", err
	}

	if ready {
		stdout, stderr, err := r.execQuery(ctx, state)
		if err != nil {
			return "", err
		}

		r.logger.Debug().Str("run_id", state.runID).Msg("query has been executed")

		output = combineOutput(stdout, stderr)
	}

	// Sanitizer/debug builds print sanitizer reports (data races, use-after-free, UB, ...) to
	// the server's stderr, which the client never sees. Capture it and forward it to the user.
	if !state.buildType.IsRelease() {
		if report := r.collectSanitizerReport(ctx, state); report != "" {
			if output != "" {
				output += "\n\n"
			}
			output += report
		}
	}

	return output, nil
}

// waitUntilReady waits until ClickHouse successfully responds to the probe query.
func (r *Runner) waitUntilReady(ctx context.Context, state *requestState) (ready bool, lastOutput string, err error) {
	deadline := time.NewTimer(r.cfg.ReadyTimeout)
	defer deadline.Stop()

	for {
		stdout, stderr, exitCode, err := r.execCommand(ctx, state, []string{"clickhouse", "client", "--query", "SELECT 1"})
		if err != nil {
			return false, "", err
		}

		if exitCode == 0 {
			return true, "", nil
		}

		select {
		case <-deadline.C:
			r.logger.Warn().Str("run_id", state.runID).Str("stderr", stderr).Msg("database has not become ready in time")
			return false, combineOutput(stdout, stderr), nil
		case <-time.NewTimer(r.cfg.ExecRetryDelay).C:
			continue
		}
	}
}

func (r *Runner) execCommand(ctx context.Context, state *requestState, args []string) (stdout string, stderr string, exitCode int, err error) {
	invokedAt := time.Now()
	defer func() {
		r.pipelineMetr.ExecCommand(err == nil, state.version, invokedAt)
	}()

	resp, execID, err := r.engine.exec(ctx, state.containerID, args)
	if err != nil {
		return "", "", 0, errors.Wrap(err, "exec failed")
	}
	defer resp.Close()

	// https://github.com/moby/moby/blob/8e610b2b55bfd1bfa9436ab110d311f5e8a74dcb/integration/internal/container/exec.go#L38
	var outBuf, errBuf bytes.Buffer
	outputDone := make(chan error)

	go func() {
		_, err = stdcopy.StdCopy(&outBuf, &errBuf, resp.Reader)
		outputDone <- err
	}()

	select {
	case err := <-outputDone:
		if err != nil {
			return "", "", 0, errors.Wrap(err, "failed to get output")
		}

	case <-ctx.Done():
		return "", "", 0, ctx.Err()
	}

	exitCode, err = r.engine.execExitCode(ctx, execID)
	if err != nil {
		return "", "", 0, err
	}

	r.logger.Debug().Str("run_id", state.runID).Dur("elapsed_ms", time.Since(invokedAt)).Msg("exec finished")

	return outBuf.String(), errBuf.String(), exitCode, nil
}

func combineOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}

	return stdout + "\n" + stderr
}

// collectSanitizerReport reads the container's stderr and returns any sanitizer report found.
func (r *Runner) collectSanitizerReport(ctx context.Context, state *requestState) string {
	stderr, err := r.engine.getContainerStderr(ctx, state.containerID)
	if err != nil {
		r.logger.Debug().Err(err).Str("run_id", state.runID).Msg("failed to read container stderr for sanitizer report")
		return "<failed to collect stderr>"
	}

	return extractSanitizerReport(stderr)
}
