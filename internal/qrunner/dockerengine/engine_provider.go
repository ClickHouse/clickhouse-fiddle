package dockerengine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/docker/cli/cli/connhelper"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	dockercli "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/pkg/errors"
)

const DefaultDockerTimeout = 5 * time.Minute

// engineProvider simplifies communication with Docker Engine API.
type engineProvider struct {
	mainCtx context.Context
	cli     *dockercli.Client

	// buildCli is a separate client used for image builds. Builds stream output for the
	// whole build duration, so it must not carry the short per-request timeout that cli
	// uses; build deadlines are enforced via the context instead.
	buildCli *dockercli.Client
}

func newProvider(ctx context.Context, daemonURL *string) (*engineProvider, error) {
	opts, err := getDockerEngineOpts(daemonURL, DefaultDockerTimeout)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build options for Docker client")
	}

	cli, err := dockercli.NewClientWithOpts(opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Docker client")
	}

	// A client without an HTTP timeout for long-running image builds.
	buildOpts, err := getDockerEngineOpts(daemonURL, 0)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build options for Docker build client")
	}

	buildCli, err := dockercli.NewClientWithOpts(buildOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Docker build client")
	}

	return &engineProvider{
		mainCtx:  ctx,
		cli:      cli,
		buildCli: buildCli,
	}, nil
}

func getDockerEngineOpts(daemonURL *string, timeout time.Duration) ([]dockercli.Opt, error) {
	opts := []dockercli.Opt{
		dockercli.WithAPIVersionNegotiation(),
	}
	if timeout > 0 {
		opts = append(opts, dockercli.WithTimeout(timeout))
	}

	if daemonURL == nil {
		return opts, nil
	}

	// Set 'StrictHostKeyChecking=no' to simplify startup in Docker containers.
	helper, err := connhelper.GetConnectionHelperWithSSHOpts(*daemonURL, []string{"-o", "StrictHostKeyChecking=no"})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ssh connection")
	}
	if helper == nil {
		return nil, errors.Wrap(err, "provided daemon_url cannot be recognized by Docker lib")
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: helper.Dialer,
		},
	}

	opts = append(opts,
		dockercli.WithHTTPClient(httpClient),
		dockercli.WithHost(helper.Host),
		dockercli.WithDialContext(helper.Dialer),
	)

	return opts, nil
}

func (p *engineProvider) ping(ctx context.Context) error {
	_, err := p.cli.Ping(ctx)

	return err
}

func (p *engineProvider) ownershipLabelFilter() (key, value string) {
	return "label", LabelOwnership
}

func (p *engineProvider) pullImage(ctx context.Context, imageTag string) (io.ReadCloser, error) {
	return p.cli.ImagePull(ctx, imageTag, image.PullOptions{})
}

func (p *engineProvider) addImageTag(ctx context.Context, existingImageTag, newImageTag string) error {
	return p.cli.ImageTag(ctx, existingImageTag, newImageTag)
}

// buildImage builds an image from the given tar build context and tags it with imageTag.
// It blocks until the build finishes and returns an error if the build fails. onLine, if not
// nil, is called with each non-empty output line so callers can report progress.
func (p *engineProvider) buildImage(ctx context.Context, buildContext io.Reader, imageTag string, buildArgs map[string]*string, onLine func(string)) error {
	resp, err := p.buildCli.ImageBuild(ctx, buildContext, types.ImageBuildOptions{
		Tags:        []string{imageTag},
		Dockerfile:  dockerfileName,
		BuildArgs:   buildArgs,
		Remove:      true,
		ForceRemove: true,
		// Don't force-pull the base image on every build; it is pulled if missing. Avoids a
		// per-build round-trip to (anonymous, rate-limited) Docker Hub.
		PullParent: false,
	})
	if err != nil {
		return errors.Wrap(err, "image build request failed")
	}
	defer resp.Body.Close()

	return readBuildOutput(resp.Body, onLine)
}

// readBuildOutput drains the Docker build output stream and returns an error if the build
// reported one. The stream is a sequence of JSON messages; each carries either build step
// output ("stream") or an image-pull status ("status"). onLine receives each non-empty line.
func readBuildOutput(r io.Reader, onLine func(string)) error {
	decoder := json.NewDecoder(r)
	for {
		var msg struct {
			Stream      string `json:"stream"`
			Status      string `json:"status"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}

		err := decoder.Decode(&msg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.Wrap(err, "failed to read build output")
		}

		if msg.Error != "" {
			return errors.New(msg.Error)
		}
		if msg.ErrorDetail.Message != "" {
			return errors.New(msg.ErrorDetail.Message)
		}

		if onLine != nil {
			if line := msg.Stream; line != "" {
				onLine(line)
			} else if msg.Status != "" {
				onLine(msg.Status)
			}
		}
	}

	return nil
}

func (p *engineProvider) getImageByID(ctx context.Context, id string) (image.InspectResponse, error) {
	inspect, err := p.cli.ImageInspect(ctx, id)

	return inspect, err
}

// getImages returns existing images.
// If filterChp is true, only created by the playground images are returned.s
func (p *engineProvider) getImages(ctx context.Context, filterChp bool) ([]image.Summary, error) {
	images, err := p.cli.ImageList(ctx, image.ListOptions{
		All: true,
	})

	if err != nil || !filterChp {
		return images, err
	}

	for i := 0; i < len(images); i++ {
		var matched bool
		for _, tag := range images[i].RepoTags {
			if IsPlaygroundImageName(tag) {
				matched = true
				break
			}
		}

		// If it's not chp-image, swap if with the last element and pop it in O(1).
		if !matched {
			images[i] = images[len(images)-1]
			images = images[:len(images)-1]
			i--
		}
	}

	return images, nil
}

func (p *engineProvider) removeImage(ctx context.Context, tag string, pruneChildren bool) ([]image.DeleteResponse, error) {
	return p.cli.ImageRemove(ctx, tag, image.RemoveOptions{
		PruneChildren: pruneChildren,
	})
}

func (p *engineProvider) createContainer(ctx context.Context, config *container.Config, hostConfig *container.HostConfig) (container.CreateResponse, error) {
	return p.cli.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
}

func (p *engineProvider) startContainer(ctx context.Context, id string) error {
	return p.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (p *engineProvider) pauseContainer(ctx context.Context, id string) error {
	return p.cli.ContainerPause(ctx, id)
}

func (p *engineProvider) unpauseContainer(ctx context.Context, id string) error {
	return p.cli.ContainerUnpause(ctx, id)
}

// exec executes the given command in the container and attaches to it.
// Keep in mind that you have to close the returned response.
func (p *engineProvider) exec(ctx context.Context, containerID string, cmd []string) (types.HijackedResponse, string, error) {
	exec, err := p.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStderr: true,
		AttachStdout: true,
		Cmd:          cmd,
	})
	if err != nil {
		return types.HijackedResponse{}, "", errors.Wrap(err, "exec create failed")
	}

	resp, err := p.cli.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return types.HijackedResponse{}, "", errors.Wrap(err, "exec attach failed")
	}

	return resp, exec.ID, nil
}

// execExitCode must be called after the exec's output has been fully drained.
func (p *engineProvider) execExitCode(ctx context.Context, execID string) (int, error) {
	inspect, err := p.cli.ContainerExecInspect(ctx, execID)
	if err != nil {
		return 0, errors.Wrap(err, "exec inspect failed")
	}

	return inspect.ExitCode, nil
}

// getContainerStderr returns the container's stderr stream. For non-TTY containers the log
// stream is multiplexed, so it is demuxed and only the stderr portion is returned. The
// clickhouse-server process writes sanitizer reports to stderr.
func (p *engineProvider) getContainerStderr(ctx context.Context, containerID string) (string, error) {
	reader, err := p.cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: false,
		ShowStderr: true,
	})
	if err != nil {
		return "", errors.Wrap(err, "failed to get container logs")
	}
	defer reader.Close()

	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, reader); err != nil {
		return "", errors.Wrap(err, "failed to demux container logs")
	}

	return errBuf.String(), nil
}

func (p *engineProvider) getContainers(ctx context.Context) ([]container.Summary, error) {
	return p.cli.ContainerList(ctx, container.ListOptions{
		Size:    true,
		All:     true,
		Limit:   -1,
		Filters: filters.NewArgs(filters.Arg(p.ownershipLabelFilter())),
	})
}

func (p *engineProvider) removeContainer(ctx context.Context, id string) error {
	return p.cli.ContainerRemove(ctx, id, container.RemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	})
}

func (p *engineProvider) pruneContainers(ctx context.Context) (container.PruneReport, error) {
	return p.cli.ContainersPrune(ctx, filters.NewArgs(filters.Arg(p.ownershipLabelFilter())))
}
