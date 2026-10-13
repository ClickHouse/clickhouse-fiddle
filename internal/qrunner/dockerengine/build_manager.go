package dockerengine

import (
	"archive/tar"
	"bytes"
	"context"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/cibuild"
	"github.com/lodthe/clickhouse-playground/internal/qrunner"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

// retryCooldown is how long a failed build is reported as failed before EnsureImage
// will start a fresh attempt. It prevents poll loops from rebuilding a failing image
// repeatedly while still allowing eventual retries.
const retryCooldown = time.Minute

// File modes for the entries written into the build-context tar.
const (
	tarFileMode       = 0o644
	tarExecutableMode = 0o755
)

type buildRecord struct {
	state      qrunner.ImageState
	stage      string
	logLines   []string
	err        error
	finishedAt time.Time
}

// Bounds on the build log tail kept in memory and streamed to the client.
const (
	maxLogLines   = 200
	maxLogLineLen = 500
	logLineSep    = "\n"
)

// Build stages reported to the user while an image is being built.
const (
	stageStarting   = "Starting build"
	stageQueued     = "Waiting in build queue"
	stagePreparing  = "Preparing build"
	stagePullBase   = "Pulling base image"
	stageDownload   = "Downloading packages"
	stageInstall    = "Installing packages"
	stageFinalizing = "Finalizing image"
)

// buildManager builds and caches debug/sanitizer images locally, ensuring at most one
// in-flight build per image and bounding overall build concurrency.
type buildManager struct {
	mainCtx  context.Context
	logger   zerolog.Logger
	engine   *engineProvider
	resolver cibuild.Resolver
	cfg      BuildSettings

	sem chan struct{} // bounds concurrent builds; nil means unlimited

	mu      sync.Mutex
	records map[string]*buildRecord // keyed by image FQN
}

func newBuildManager(ctx context.Context, logger zerolog.Logger, engine *engineProvider, resolver cibuild.Resolver, cfg BuildSettings) *buildManager {
	var sem chan struct{}
	if cfg.MaxConcurrent > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultBuildTimeout
	}

	return &buildManager{
		mainCtx:  ctx,
		logger:   logger.With().Str("component", "image_builder").Logger(),
		engine:   engine,
		resolver: resolver,
		cfg:      cfg,
		sem:      sem,
		records:  make(map[string]*buildRecord),
	}
}

func (m *buildManager) enabled() bool {
	return m != nil && m.cfg.Enabled && m.resolver != nil
}

// resolve maps a (version, build type) to its image FQN and build spec.
func (m *buildManager) resolve(ctx context.Context, version string, bt buildtype.BuildType) (fqn string, spec cibuild.BuildSpec, err error) {
	spec, err = m.resolver.Resolve(ctx, version, bt)
	if err != nil {
		return "", cibuild.BuildSpec{}, err
	}

	return PlaygroundBuildImageName(bt, version, spec.DownloadURLs), spec, nil
}

func (m *buildManager) imageExists(ctx context.Context, fqn string) bool {
	_, err := m.engine.getImageByID(ctx, fqn)

	return err == nil
}

// Ensure makes sure the image for the given version and build type is present, starting a
// background build if necessary. It is non-blocking and returns the current status.
func (m *buildManager) Ensure(ctx context.Context, version string, bt buildtype.BuildType) (qrunner.ImageStatus, error) {
	if !m.enabled() {
		return qrunner.ImageStatus{}, qrunner.ErrBuildsNotSupported
	}

	fqn, spec, err := m.resolve(ctx, version, bt)
	if err != nil {
		return qrunner.ImageStatus{}, err
	}

	if m.imageExists(ctx, fqn) {
		return qrunner.ImageStatus{State: qrunner.ImageReady}, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rec := m.records[fqn]
	switch {
	case rec != nil && rec.state == qrunner.ImageBuilding:
		return qrunner.ImageStatus{
			State:  qrunner.ImageBuilding,
			Detail: rec.stage,
			Logs:   strings.Join(rec.logLines, logLineSep),
		}, nil

	case rec != nil && rec.state == qrunner.ImageFailed && time.Since(rec.finishedAt) < retryCooldown:
		return qrunner.ImageStatus{
			State: qrunner.ImageFailed,
			Error: errString(rec.err),
			Logs:  strings.Join(rec.logLines, logLineSep),
		}, nil
	}

	m.records[fqn] = &buildRecord{state: qrunner.ImageBuilding, stage: stageStarting}
	go m.build(fqn, version, bt, spec)

	return qrunner.ImageStatus{State: qrunner.ImageBuilding, Detail: stageStarting}, nil
}

// ansiEscapeRe matches ANSI/CSI escape sequences (e.g. wget's colored progress output).
var ansiEscapeRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

// setStage sets the reported progress stage of an in-flight build directly (used for
// transitions that have no corresponding build-output line, e.g. queueing).
func (m *buildManager) setStage(fqn, stage string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if rec := m.records[fqn]; rec != nil && rec.state == qrunner.ImageBuilding {
		rec.stage = stage
	}
}

// onBuildLine records a build output chunk: it advances the progress stage (if a line
// carries a stage signal) and appends the cleaned lines to the streamed log tail. A single
// Docker stream chunk may contain several lines and carriage returns (wget progress), so it
// is split into individual non-empty lines; consecutive progress-bar lines are collapsed to
// a single updating line to avoid flooding the log.
func (m *buildManager) onBuildLine(fqn, chunk string) {
	lines, stage := parseBuildChunk(chunk)
	if len(lines) == 0 && stage == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rec := m.records[fqn]
	if rec == nil || rec.state != qrunner.ImageBuilding {
		return
	}

	if stage != "" {
		rec.stage = stage
	}
	rec.appendLogs(lines)
}

// parseBuildChunk splits a Docker stream chunk into cleaned, non-empty log lines and the last
// progress stage signaled by any of them.
func parseBuildChunk(chunk string) (lines []string, stage string) {
	chunk = ansiEscapeRe.ReplaceAllString(chunk, "")
	chunk = strings.ReplaceAll(chunk, "\r", logLineSep)

	for line := range strings.SplitSeq(chunk, logLineSep) {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > maxLogLineLen {
			line = line[:maxLogLineLen]
		}
		if s := classifyBuildStage(line); s != "" {
			stage = s
		}
		lines = append(lines, line)
	}

	return lines, stage
}

// appendLogs adds lines to the record's log tail, collapsing consecutive progress-bar updates
// into one live-updating line, and trims the tail to the cap.
func (r *buildRecord) appendLogs(lines []string) {
	for _, line := range lines {
		n := len(r.logLines)
		if isProgressLine(line) && n > 0 && isProgressLine(r.logLines[n-1]) {
			r.logLines[n-1] = line
			continue
		}
		r.logLines = append(r.logLines, line)
	}
	if len(r.logLines) > maxLogLines {
		r.logLines = r.logLines[len(r.logLines)-maxLogLines:]
	}
}

// isProgressLine reports whether a log line is a wget progress-bar update.
func isProgressLine(line string) bool {
	return strings.Contains(line, "%[") || strings.Contains(line, "MB/s") || strings.Contains(line, "KB/s")
}

// classifyBuildStage maps a Docker build output line to a user-facing stage, or "" if the
// line carries no stage signal (the current stage is then kept).
func classifyBuildStage(line string) string {
	switch {
	case strings.Contains(line, "installing from custom predefined urls"),
		strings.Contains(line, ".deb"):
		return stageDownload
	case strings.Contains(line, "Selecting previously unselected"),
		strings.Contains(line, "Preparing to unpack"),
		strings.Contains(line, "Unpacking "),
		strings.Contains(line, "Setting up "):
		return stageInstall
	case strings.Contains(line, "system.build_options"),
		strings.Contains(line, "locale-gen"),
		strings.Contains(line, "exporting to"),
		strings.Contains(line, "naming to"),
		strings.Contains(line, "Successfully tagged"):
		return stageFinalizing
	case strings.Contains(line, "Pulling from"),
		strings.Contains(line, "Pulling fs layer"),
		strings.Contains(line, "Pull complete"),
		strings.Contains(line, "Already exists"),
		strings.Contains(line, "Extracting"):
		return stagePullBase
	default:
		return ""
	}
}

func (m *buildManager) build(fqn, version string, bt buildtype.BuildType, spec cibuild.BuildSpec) {
	if m.sem != nil {
		// Tell the user the build is queued (another build holds the slot) instead of
		// leaving it on "Starting build" with no output.
		m.setStage(fqn, stageQueued)
		select {
		case m.sem <- struct{}{}:
			defer func() { <-m.sem }()
		case <-m.mainCtx.Done():
			// TODO: One context failure should not fail all concurrent requests for the same FQN.
			m.finish(fqn, m.mainCtx.Err())
			return
		}
	}

	m.setStage(fqn, stagePreparing)

	ctx, cancel := context.WithTimeout(m.mainCtx, m.cfg.Timeout)
	defer cancel()

	startedAt := time.Now()
	m.logger.Info().Str("image", fqn).Str("version", version).Str("build_type", bt.String()).Msg("starting image build")

	buildCtx, err := buildTarContext()
	if err != nil {
		m.finish(fqn, errors.Wrap(err, "failed to assemble build context"))
		return
	}

	args := buildArgs(spec)
	err = m.engine.buildImage(ctx, buildCtx, fqn, args, func(line string) {
		m.onBuildLine(fqn, line)
	})
	if err != nil {
		m.logger.Error().Err(err).Str("image", fqn).Msg("image build failed")
		m.finish(fqn, err)

		return
	}

	m.logger.Info().Str("image", fqn).Dur("elapsed", time.Since(startedAt)).Msg("image build finished")
	m.finish(fqn, nil)
}

func (m *buildManager) finish(fqn string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec := m.records[fqn]
	if rec == nil {
		rec = &buildRecord{}
		m.records[fqn] = rec
	}

	rec.finishedAt = time.Now()
	rec.err = err
	if err != nil {
		// Keep the log tail so the client can surface why the build failed.
		rec.state = qrunner.ImageFailed
	} else {
		rec.state = qrunner.ImageReady
		rec.logLines = nil
	}
}

// buildArgs assembles the Docker build args for a non-release build.
func buildArgs(spec cibuild.BuildSpec) map[string]*string {
	urls := strings.Join(spec.DownloadURLs, " ")
	version := spec.Version
	// The clickhouse-builds S3 bucket intermittently returns 5xx during downloads, so use a
	// more generous retry budget than the Dockerfile's defaults (5 retries x 1s).
	wgetRetries := "10"
	wgetRetryDelay := "5"

	return map[string]*string{
		"DIRECT_DOWNLOAD_URLS": &urls,
		"VERSION":              &version,
		"WGET_RETRIES":         &wgetRetries,
		"WGET_RETRY_DELAY":     &wgetRetryDelay,
	}
}

// buildTarContext builds an in-memory tar archive of the embedded build context.
func buildTarContext() (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	entries, err := buildContextFS.ReadDir(buildContextDir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read embedded build context")
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		err = writeTarFile(tw, entry)
		if err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, errors.Wrap(err, "failed to close tar writer")
	}

	return buf, nil
}

func writeTarFile(tw *tar.Writer, entry fs.DirEntry) error {
	data, err := buildContextFS.ReadFile(path.Join(buildContextDir, entry.Name()))
	if err != nil {
		return errors.Wrapf(err, "failed to read %s", entry.Name())
	}

	var mode int64 = tarFileMode
	if strings.HasSuffix(entry.Name(), ".sh") {
		mode = tarExecutableMode
	}

	hdr := &tar.Header{
		Name: entry.Name(),
		Mode: mode,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return errors.Wrapf(err, "failed to write tar header for %s", entry.Name())
	}
	if _, err := tw.Write(data); err != nil {
		return errors.Wrapf(err, "failed to write tar body for %s", entry.Name())
	}

	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}
