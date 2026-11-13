package stubrunner

import (
	"context"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/qrunner"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"

	"github.com/pkg/errors"
)

type Run = func(ctx context.Context, run *queryrun.Run) (qrunner.QueryOutput, error)

var StubRun = func(ctx context.Context, run *queryrun.Run) (qrunner.QueryOutput, error) {
	return qrunner.QueryOutput{}, errors.New("stub cannot run queries")
}

// Runner is a stub runner for tests.
type Runner struct {
	ctx  context.Context
	name string

	run Run
}

func New(ctx context.Context, name string, run Run) *Runner {
	return &Runner{
		ctx:  ctx,
		name: name,
		run:  run,
	}
}

func (r *Runner) Type() qrunner.Type {
	return qrunner.TypeStub
}

func (r *Runner) Name() string {
	return r.name
}

func (r *Runner) Status(_ context.Context) qrunner.RunnerStatus {
	return qrunner.RunnerStatus{
		Alive: true,
	}
}

func (r *Runner) Start() error {
	return nil
}

func (r *Runner) Stop(_ context.Context) error {
	return nil
}

func (r *Runner) RunQuery(ctx context.Context, run *queryrun.Run) (qrunner.QueryOutput, error) {
	return r.run(ctx, run)
}

func (r *Runner) EnsureImage(_ context.Context, _ string, bt buildtype.BuildType) (qrunner.ImageStatus, error) {
	if bt.IsRelease() {
		return qrunner.ImageStatus{State: qrunner.ImageReady}, nil
	}

	return qrunner.ImageStatus{}, qrunner.ErrBuildsNotSupported
}
