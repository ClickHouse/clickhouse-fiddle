package dockerengine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/dbsettings/runsettings"
	"github.com/lodthe/clickhouse-playground/internal/dockertag"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
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
		},
	}

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
	}

	rcfg := DefaultConfig
	runner, _ := New(ctx, logger, "Test", rcfg, tagStorage, nil)

	for _, tc := range cases {
		output, err := runner.RunQuery(ctx, &queryrun.Run{Input: tc.query, Version: tc.version, Database: tc.database, Settings: tc.runSettings})
		if err != nil {
			t.Log(err.Error())
		}
		assert.Contains(t, output, tc.expectedOutput, "output doesn't contain expected results")
	}

	t.Cleanup(func() {
		//Fetch all remaining test containers
		containers, err := runner.engine.cli.ContainerList(ctx, container.ListOptions{
			Filters: filters.NewArgs(filters.Arg("label", "clickhouse.playground.runner=Test")),
		})
		assert.NoError(t, err)

		//Remove all remaining test containers
		for _, c := range containers {
			err = runner.engine.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{
				RemoveVolumes: true,
				Force:         true,
			})
			if err != nil && strings.Contains(err.Error(), "is already in progress") {
				continue
			}
			assert.NoError(t, err)
		}
	})
}
