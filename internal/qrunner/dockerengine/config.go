package dockerengine

import "time"

type Config struct {
	DaemonURL *string

	ExecRetryDelay time.Duration
	ReadyTimeout   time.Duration

	DefaultOutputFormat string

	// Path to the xml or yaml config which will be mounted to the ../config.d/ directory.
	CustomConfigPath *string

	// Path to the quotas config that will be mounted to the ../users.d/ directory.
	// https://clickhouse.com/docs/en/operations/quotas/
	QuotasPath *string

	// Path to the config enabling the in-process ClickHouse Keeper. It is mounted to
	// the ../config.d/ directory only for builds that support the embedded Keeper
	// (see keeperConfigSupported), so coordination-dependent features work:
	// ReplicatedMergeTree, ON CLUSTER, KeeperMap, system.zookeeper.
	KeeperConfigPath *string

	GC *GCConfig

	MaxWarmContainers         uint
	StatusCollectionFrequency time.Duration

	Container ContainerSettings

	// Builds configures local image builds for non-release (debug/sanitizer) build types.
	Builds BuildSettings
}

type BuildSettings struct {
	// Enabled turns on local image builds. When false, non-release builds are rejected.
	Enabled bool

	// Timeout bounds a single image build. Debug/sanitizer images are large and slow to
	// download and install, so this should be generous.
	Timeout time.Duration

	// MaxConcurrent limits the number of concurrent image builds. 0 means unlimited.
	MaxConcurrent uint
}

type ContainerSettings struct {
	NetworkMode *string // Network mode to use for the container.

	CPULimit    uint64 // In nano cpus (1 core = 1e9 nano cpus). If 0, then unlimited.
	CPUSet      string // A comma-separated list or hyphen-separated range of CPUs a container can use. If "", then any cores can be used.
	MemoryLimit uint64 // In bytes. If 0, then unlimited.
}

type GCConfig struct {
	// How often GC will be triggered.
	TriggerFrequency time.Duration

	// During the container garbage collection, all containers created
	// before (time.Now() - ContainerTTL) will be removed.
	// If ContainerTTL is nil, containers are not force removed.
	ContainerTTL *time.Duration

	// Image gc triggers when there are at least ImageGCCountThreshold downloaded chp images.
	// After the garbage collection, at most ImageBufferSize least recently tagged images will be left.
	// If ImageGCCountThreshold is missed, images are not pruned.
	ImageGCCountThreshold *uint
	ImageBufferSize       uint
}

var defaultContainerTTL = 60 * time.Second
var defaultImageGCCountThreshold = uint(60)
var defaultImageBufferSize = uint(30)

var DefaultConfig = Config{
	ExecRetryDelay: 200 * time.Millisecond,
	ReadyTimeout:   30 * time.Second,

	DefaultOutputFormat: "TabSeparated",

	CustomConfigPath: nil,
	QuotasPath:       nil,
	KeeperConfigPath: nil,

	GC: &GCConfig{
		TriggerFrequency:      5 * time.Minute,
		ContainerTTL:          &defaultContainerTTL,
		ImageGCCountThreshold: &defaultImageGCCountThreshold,
		ImageBufferSize:       defaultImageBufferSize,
	},

	MaxWarmContainers:         5,
	StatusCollectionFrequency: 30 * time.Second,

	Container: ContainerSettings{
		NetworkMode: nil,
		CPULimit:    2 * 1e9,
		CPUSet:      "",
		MemoryLimit: 1 * 1e9,
	},

	Builds: BuildSettings{
		Enabled:       false,
		Timeout:       DefaultBuildTimeout,
		MaxConcurrent: 1,
	},
}

// DefaultBuildTimeout bounds a single local image build.
const DefaultBuildTimeout = 45 * time.Minute
