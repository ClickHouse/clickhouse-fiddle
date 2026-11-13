package dockerengine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/dbsettings/runsettings"
	"github.com/lodthe/clickhouse-playground/internal/dockertag"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tagStorageMock struct {
	images map[string]dockertag.Image
}

func (t tagStorageMock) Find(version string) (dockertag.Image, bool) {
	if _, ok := t.images[version]; !ok {
		return dockertag.Image{}, false
	}

	return t.images[version], true
}

func TestKeeperConfigSupported(t *testing.T) {
	cases := []struct {
		buildType buildtype.BuildType
		version   string
		expected  bool
	}{
		{buildtype.Release, "20", false},
		{buildtype.Release, "21.8", false},
		{buildtype.Release, "22.1.3", false},
		{buildtype.Release, "22.3", true},
		{buildtype.Release, "22.3-alpine", true},
		{buildtype.Release, "22.8.1.2097", true},
		{buildtype.Release, "23", true},
		{buildtype.Release, "head", true},
		{buildtype.Release, "latest", true},
		{buildtype.Debug, "55555", true},
		{buildtype.TSAN, "head-1a2b3c", true},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s-%s", tc.buildType, tc.version), func(t *testing.T) {
			assert.Equal(t, tc.expected, keeperConfigSupported(tc.buildType, tc.version))
		})
	}
}

func TestCustomSettings(t *testing.T) {
	if os.Getenv("RUN_DOCKER_TESTS") == "" {
		t.Skip("Skipping a docker test. Set RUN_DOCKER_TESTS=true to enable.")
	}

	ctx := context.Background()
	runner := newTestRunner(ctx, t)

	cases := []struct {
		database       string
		version        string
		query          string
		expectedOutput string
		runSettings    runsettings.RunSettings
	}{
		{
			database:       "clickhouse",
			version:        "21",
			query:          "SELECT 1",
			expectedOutput: "1\n",
			runSettings:    &runsettings.ClickHouseSettings{},
		},
		{
			database:       "clickhouse",
			version:        "21",
			query:          "SELECT 1",
			expectedOutput: "┌─1─┐\n│ 1 │\n└───┘\n",
			runSettings:    &runsettings.ClickHouseSettings{OutputFormat: "PrettyCompactMonoBlock"},
		},
		{
			database:       "clickhouse",
			version:        "20",
			query:          "SELECT 1",
			expectedOutput: "1\n",
			runSettings:    &runsettings.ClickHouseSettings{OutputFormat: "PrettyCompactMonoBlock"},
		},
		{
			database:       "clickhouse",
			version:        "latest",
			query:          "SELECT 1",
			expectedOutput: "1\n",
			runSettings:    &runsettings.ClickHouseSettings{},
		},
	}

	for _, tc := range cases {
		output, err := runner.RunQuery(ctx, &queryrun.Run{Input: tc.query, Version: tc.version, Database: tc.database, Settings: tc.runSettings})
		if err != nil {
			t.Log(err.Error())
		}
		assert.Contains(t, output.Output, tc.expectedOutput, "output doesn't contain expected results")
	}
}

// The server creates the system.*_log tables lazily, several seconds after startup.
// A run must be able to query them from its very first statement anyway.
func TestSystemLogTablesExistFromTheStart(t *testing.T) {
	if os.Getenv("RUN_DOCKER_TESTS") == "" {
		t.Skip("Skipping a docker test. Set RUN_DOCKER_TESTS=true to enable.")
	}

	ctx := context.Background()
	runner := newTestRunner(ctx, t)

	const query = "EXISTS TABLE system.query_log"
	run := func(version string) string {
		output, err := runner.RunQuery(ctx, &queryrun.Run{
			Input:    query,
			Version:  version,
			Database: "clickhouse",
			Settings: &runsettings.ClickHouseSettings{},
		})
		require.NoError(t, err)

		return output.Output
	}

	// The prewarmer is not started yet, so every run gets a container that has just been created.
	for _, version := range []string{"20", "21", "latest"} {
		assert.Equal(t, "1\n", run(version), "fresh container, version %s", version)
	}

	// Now let the prewarmer prepare a spare container and check a run that takes it.
	const version = "latest"
	_, imageFQN, err := runner.resolveImage(ctx, version, buildtype.Release)
	require.NoError(t, err)

	require.NoError(t, runner.Start())
	t.Cleanup(func() {
		_ = runner.Stop(ctx)
	})

	// The runs above have queued their images already. The explicit request keeps this part
	// independent of them.
	runner.prewarmer.PushNewRequest(requestState{version: version, buildType: buildtype.Release, imageFQN: imageFQN})

	require.Eventually(t, func() bool {
		runner.prewarmer.lock.Lock()
		c, found := runner.prewarmer.containers[imageFQN]
		runner.prewarmer.lock.Unlock()
		if !found {
			return false
		}

		release := c.acquireLock()
		defer release()

		return c.status == statusPaused && c.systemLogsPrepared
	}, time.Minute, 100*time.Millisecond, "the prewarmer has not paused a prepared container")

	assert.Equal(t, "1\n", run(version), "prewarmed container")
}

// newTestRunner creates a runner that can start the images listed below.
// Containers left by the test are removed when it finishes.
func newTestRunner(ctx context.Context, t *testing.T) *Runner {
	t.Helper()

	logger := zlog.Logger.Level(zerolog.ErrorLevel)

	tagStorage := tagStorageMock{
		images: map[string]dockertag.Image{
			"21": {
				Repository: "yandex/clickhouse-server",
				Tag:        "21",
				Digest:     "sha256:edfee043e4f909dd471c6e282ce3cfd0ce90a4cad3fc234cb27633debe26ea05",
			},
			"20": {
				Repository: "yandex/clickhouse-server",
				Tag:        "21",
				Digest:     "sha256:c03c136ca0e87f9b821718f05dc45dea413946ff650ad15980ea89d1c34c87d3",
			},
			// Moving tag: keeps readiness detection exercised against current clickhouse-client output.
			"latest": {
				Repository: "clickhouse/clickhouse-server",
				Tag:        "latest",
			},
		},
	}

	// Runner metrics are registered per runner name, so every test needs a name of its own.
	name := t.Name()

	runner, err := New(ctx, logger, name, DefaultConfig, tagStorage, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		//Fetch all remaining test containers
		containers, err := runner.engine.cli.ContainerList(ctx, container.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", "clickhouse.playground.runner="+name)),
		})
		assert.NoError(t, err)

		//Remove all remaining test containers
		for _, c := range containers {
			err = runner.engine.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{
				RemoveVolumes: true,
				Force:         true,
			})
			if err != nil && (strings.Contains(err.Error(), "is already in progress") || cerrdefs.IsNotFound(err)) {
				continue
			}
			assert.NoError(t, err)
		}
	})

	return runner
}

func TestOutputIsCutAtTheCap(t *testing.T) {
	if os.Getenv("RUN_DOCKER_TESTS") == "" {
		t.Skip("Skipping a docker test. Set RUN_DOCKER_TESTS=true to enable.")
	}

	ctx := context.Background()
	runner := newTestRunner(ctx, t)
	runner.cfg.MaxOutputBytes = 10_000

	// About 78 MB of output; the runner must stop reading at the cap and return quickly.
	output, err := runner.RunQuery(ctx, &queryrun.Run{
		Input:    "SELECT number FROM numbers(10000000)",
		Version:  "latest",
		Database: "clickhouse",
		Settings: &runsettings.ClickHouseSettings{},
	})
	require.NoError(t, err)

	require.True(t, output.Truncated)
	require.LessOrEqual(t, len(output.Output), 10_000)
	require.True(t, strings.HasPrefix(output.Output, "0\n1\n2\n"), "the beginning of the output is kept")
}

func TestLongQueryIsAcceptedOverStdin(t *testing.T) {
	if os.Getenv("RUN_DOCKER_TESTS") == "" {
		t.Skip("Skipping a docker test. Set RUN_DOCKER_TESTS=true to enable.")
	}

	ctx := context.Background()
	runner := newTestRunner(ctx, t)

	// 200 KB: above the 128 KiB the kernel allows for one command-line argument, below
	// ClickHouse's own default max_query_size of 256 KiB.
	query := "SELECT length('" + strings.Repeat("x", 200_000) + "')"
	output, err := runner.RunQuery(ctx, &queryrun.Run{
		Input:    query,
		Version:  "latest",
		Database: "clickhouse",
		Settings: &runsettings.ClickHouseSettings{},
	})
	require.NoError(t, err)

	require.Equal(t, "200000\n", output.Output)
	require.False(t, output.Truncated)
}
