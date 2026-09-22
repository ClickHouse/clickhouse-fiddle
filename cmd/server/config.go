package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lodthe/clickhouse-playground/internal/dockertag"
	"github.com/lodthe/clickhouse-playground/internal/qrunner/coordinator"

	"github.com/aws/aws-sdk-go-v2/aws"
	gconfig "github.com/gookit/config/v2"
	gyaml "github.com/gookit/config/v2/yaml"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	zlog "github.com/rs/zerolog/log"
)

const DefaultConfigPath = "config.yml"
const DefaultMaxQueryLength = 2500
const DefaultMaxOutputLength = 25000

type RunnerType string

const (
	RunnerTypeDockerEngine RunnerType = "DOCKER_ENGINE"
)

type LogFormat string

const (
	PrettyLogFormat LogFormat = "pretty"
	JSONLogFormat   LogFormat = "json"
)

type Config struct {
	LogLevel  string    `mapstructure:"log_level"`
	LogFormat LogFormat `mapstructure:"log_format"`

	DockerImage DockerImage `mapstructure:"docker_image"`

	API API `mapstructure:"api"`

	Settings CHSettings `mapstucture:"settings"`
	Limits   Limits     `mapstructure:"limits"`

	PrometheusExportAddress string `mapstructure:"prometheus_address"`

	AWS AWS `mapstructure:"aws"`

	Coordinator Coordinator `mapstructure:"coordinator"`
	Runners     []Runner    `mapstructure:"runners"`
}

type CHSettings struct {
	DefaultFormat *string `mapstructure:"default_format"`
}

type Limits struct {
	MaxQueryLength  uint64 `mapstructure:"max_query_length"`
	MaxOutputLength uint64 `mapstructure:"max_output_length"`
}

type DockerAuth struct {
	Identifier string `mapstructure:"identifier"`
	Secret     string `mapstructure:"secret"`
}

type DockerImage struct {
	Auth                DockerAuth    `mapstructure:"auth"`
	Repositories        []string      `mapstructure:"repositories"`
	CacheExpirationTime time.Duration `mapstructure:"image_tags_cache_expiration_time"`

	Builds Builds `mapstructure:"builds"`
}

// Builds configures local image builds for non-release (debug/sanitizer) build types.
// When disabled, only release builds (pulled from Docker Hub) are available.
type Builds struct {
	Enabled bool `mapstructure:"enabled"`

	// TODO: Use *string for optional parameters.

	// ReportBaseURL is the S3 location hosting ClickHouse CI (praktika) report JSON files.
	ReportBaseURL string `mapstructure:"report_base_url"`
	// GitHubAPIURL is used to resolve a version's git tag to a commit sha.
	GitHubAPIURL string `mapstructure:"github_api_url"`
	// Repository is the ClickHouse GitHub repository ("owner/name").
	Repository string `mapstructure:"repository"`
	// GitHubToken is optional; it raises the GitHub API rate limit when set.
	GitHubToken string `mapstructure:"github_token"`

	// ResolveCacheTTL bounds how long resolved artifact URLs are cached.
	ResolveCacheTTL time.Duration `mapstructure:"resolve_cache_ttl"`
	// Timeout bounds a single image build.
	Timeout time.Duration `mapstructure:"timeout"`
	// MaxConcurrent limits concurrent image builds. 0 means unlimited.
	MaxConcurrent uint `mapstructure:"max_concurrent"`
}

type API struct {
	ListeningAddress string        `mapstructure:"address"`
	ServerTimeout    time.Duration `mapstructure:"server_timeout"`
	CacheDisabled    bool          `mapstructure:"cache_disabled"`
}

type AWS struct {
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
	Region          string `mapstructure:"region"`

	// EndpointURL overrides the DynamoDB endpoint. Leave empty for real AWS; set it to point
	// at a local DynamoDB (e.g. http://dynamodb-local:8000) for development.
	EndpointURL string `mapstructure:"endpoint_url"`

	QueryRunsTableName string `mapstructure:"query_runs_table"`
}

type Coordinator struct {
	HealthCheckRetryDelay time.Duration `mapstructure:"health_check_retry_delay"`
}

type Runner struct {
	Type           RunnerType `mapstructure:"type"`
	Name           string     `mapstructure:"name"`
	Weight         uint       `mapstructure:"weight"`
	MaxConcurrency *uint32    `mapstructure:"max_concurrency"`

	DockerEngine *DockerEngine `mapstructure:"docker_engine"`
}

type DockerEngine struct {
	DaemonURL        *string         `mapstructure:"daemon_url"`
	CustomConfigPath *string         `mapstructure:"custom_config_path"`
	QuotasPath       *string         `mapstructure:"quotas_path"`
	KeeperConfigPath *string         `mapstructure:"keeper_config_path"`
	GC               *DockerEngineGC `mapstructure:"gc"`
	Prewarm          *Prewarm        `mapsctucture:"prewarm"`

	Container ContainerSettings `mapstructure:"container"`
}

type DockerEngineGC struct {
	TriggerFrequency time.Duration `mapstructure:"trigger_frequency"`

	ContainerTTL *time.Duration `mapstructure:"container_ttl"`

	ImageGCCountThreshold *uint `mapstructure:"image_count_threshold"`
	ImageBufferSize       uint  `mapstructure:"image_buffer_size"`
}

type Prewarm struct {
	MaxWarmContainers *uint `mapstructure:"max_warm_containers"`
}

type ContainerSettings struct {
	NetworkMode   *string `mapstucture:"network_mode"`
	CPULimit      float64 `mapstructure:"cpu_limit"`
	CPUSet        string  `mapstructure:"cpu_cores_set"`
	MemoryLimitMB float64 `mapstructure:"memory_limit_mb"`
}

func (r *Runner) Validate() error {
	if r.Name == "" {
		return errors.New("runner.name is required")
	}

	if r.Weight == 0 {
		r.Weight = coordinator.DefaultWeight
		zlog.Debug().Str("runner", r.Name).Int("new_value", coordinator.DefaultWeight).Msg("weight has been set")
	}

	if r.MaxConcurrency != nil && *r.MaxConcurrency < 1 {
		return errors.Errorf("max_concurrency must be > 0, but %d has been found", *r.MaxConcurrency)
	}

	switch r.Type {
	case RunnerTypeDockerEngine:
		gc := r.DockerEngine.GC
		if gc == nil {
			break
		}

		if gc.TriggerFrequency == 0 {
			gc.TriggerFrequency = 1 * time.Minute
		}

		daemonURL := r.DockerEngine.DaemonURL
		if daemonURL != nil && !strings.HasPrefix(*daemonURL, "ssh://") {
			return errors.Errorf("[%s] runner.docker_daemon.daemon_url must be empty or start with 'ssh://', but %s found", r.Name, *daemonURL)
		}

	case "":
		return errors.Errorf("[%s] runner.type is required", r.Name)

	default:
		return errors.Errorf("unknown runner %s type %s (supported: %s)", r.Name, r.Type, RunnerTypeDockerEngine)
	}

	return nil
}

func LoadConfig() (*Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = DefaultConfigPath
	}

	gconfig.WithOptions(
		gconfig.ParseEnv,
		gconfig.Readonly,
		func(opts *gconfig.Options) {
			opts.DecoderConfig = &mapstructure.DecoderConfig{
				TagName:          "mapstructure",
				WeaklyTypedInput: true,
				DecodeHook:       mapstructure.StringToTimeDurationHookFunc(),
			}
		},
	)
	gconfig.AddDriver(gyaml.Driver)

	err := gconfig.LoadFiles(path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load config")
	}

	cfg := new(Config)
	err = gconfig.BindStruct("", cfg)
	if err != nil {
		return nil, errors.Wrap(err, "config binding failed")
	}

	err = cfg.validate()
	if err != nil {
		return nil, errors.Wrap(err, "invalid config")
	}

	return cfg, nil
}

// validate verifies the loaded config and sets default values for missed fields.
func (c *Config) validate() error {
	if c.LogLevel == "" {
		c.LogLevel = "debug"
	}

	switch c.LogFormat {
	case "":
		c.LogFormat = JSONLogFormat

	case JSONLogFormat, PrettyLogFormat:

	default:
		return fmt.Errorf("invalid log format (available: %s, %s)", JSONLogFormat, PrettyLogFormat)
	}

	// Docker Hub credentials are optional: anonymous pull tokens are issued for public
	// repositories. Provide credentials (username + personal access token) to raise the
	// registry rate limits.
	if (c.DockerImage.Auth.Identifier == "") != (c.DockerImage.Auth.Secret == "") {
		return errors.New("docker_image.auth.identifier and docker_image.auth.secret must be set together")
	}
	if c.DockerImage.Auth.Identifier == "" {
		zlog.Warn().Msg("docker_image.auth is empty; using anonymous registry pull tokens (rate limited)")
	}
	if len(c.DockerImage.Repositories) == 0 {
		return errors.New("docker_image.repositories must be non-empty")
	}
	if c.DockerImage.CacheExpirationTime == 0 {
		c.DockerImage.CacheExpirationTime = dockertag.DefaultExpirationTime
	}

	if c.API.ListeningAddress == "" {
		c.API.ListeningAddress = ":9000"
	}
	if c.API.ServerTimeout == 0 {
		c.API.ServerTimeout = 60 * time.Second
	}

	if c.Limits.MaxQueryLength == 0 {
		c.Limits.MaxQueryLength = DefaultMaxQueryLength
	}
	if c.Limits.MaxOutputLength == 0 {
		c.Limits.MaxOutputLength = DefaultMaxOutputLength
	}

	if c.PrometheusExportAddress == "" {
		c.PrometheusExportAddress = ":2112"
	}

	if c.AWS.Region == "" {
		return errors.New("aws.region is required")
	}
	if c.AWS.QueryRunsTableName == "" {
		return errors.New("aws.query_runs_table is required")
	}

	if c.Coordinator.HealthCheckRetryDelay == 0 {
		c.Coordinator.HealthCheckRetryDelay = coordinator.DefaultHealthCheckRetryDelay
	}

	if len(c.Runners) == 0 {
		return errors.New("empty runner list")
	}

	uniqueRunners := make(map[string]struct{}, len(c.Runners))
	for i := range c.Runners {
		err := c.Runners[i].Validate()
		if err != nil {
			return errors.Wrap(err, "runner validation")
		}

		_, exists := uniqueRunners[c.Runners[i].Name]
		if exists {
			return errors.Errorf("runner names must be unique, but '%s' is not unique", c.Runners[i].Name)
		}

		uniqueRunners[c.Runners[i].Name] = struct{}{}
	}

	return nil
}

func (c *Config) Retrieve(_ context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     c.AWS.AccessKeyID,
		SecretAccessKey: c.AWS.SecretAccessKey,
		Source:          "local config",
	}, nil
}
