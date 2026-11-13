package qrunner

import (
	"context"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"
)

// QueryOutput is what a query produced: the client's combined stdout and stderr, and
// whether the runner stopped reading at its output cap (see dockerengine.Config.MaxOutputBytes).
type QueryOutput struct {
	Output    string
	Truncated bool
}

type Runner interface {
	Type() Type
	Name() string

	Status(ctx context.Context) RunnerStatus

	RunQuery(ctx context.Context, run *queryrun.Run) (QueryOutput, error)

	// EnsureImage makes sure the image for the given version and build type is present,
	// building it in the background if necessary. It returns the current build status and
	// is non-blocking. For release builds it always reports ImageReady.
	EnsureImage(ctx context.Context, version string, bt buildtype.BuildType) (ImageStatus, error)

	// Start initializes background processes (like garbage collection and status exporter).
	// This function is non-blocking.
	Start() error

	// Stop stops background tasks and waits for their finish.
	Stop(shutdownCtx context.Context) error
}
