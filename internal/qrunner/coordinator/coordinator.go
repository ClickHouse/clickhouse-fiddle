package coordinator

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lodthe/clickhouse-playground/internal/buildtype"
	"github.com/lodthe/clickhouse-playground/internal/qrunner"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

const DefaultLivenessCheckTimeout = 5 * time.Second

// Coordinator is a runner that does load balancing among other runners.
// It keeps list of existing runners and dispatches incoming queries to one of them.
//
// At the moment, the coordinator just picks a random runner. There are plans to improve load balancing
// mechanisms in the future: use P2C, send health checks and monitor liveness.
type Coordinator struct {
	ctx    context.Context
	cancel context.CancelFunc

	config             Config
	livenessCheckLoops sync.WaitGroup

	logger  zerolog.Logger
	started atomic.Int32

	runners  []*Runner
	balancer *balancer
}

func New(ctx context.Context, logger zerolog.Logger, runners []*Runner, cfg Config) *Coordinator {
	ctx, cancel := context.WithCancel(ctx)

	return &Coordinator{
		ctx:      ctx,
		cancel:   cancel,
		config:   cfg,
		logger:   logger.With().Str("runner", "coordinator").Logger(),
		runners:  runners,
		balancer: newBalancer(logger),
	}
}

func (c *Coordinator) Type() qrunner.Type {
	return qrunner.TypeCoordinator
}

func (c *Coordinator) Name() string {
	return "coordinator"
}

// Start starts underlying runners and starts liveness probe processes.
func (c *Coordinator) Start() error {
	if !c.started.CompareAndSwap(0, 1) {
		return errors.New("coordinator has already been started")
	}

	c.logger.Info().Int("count", len(c.runners)).Msg("starting...")

	var totalWeight uint64
	var count uint
	for _, r := range c.runners {
		totalWeight += uint64(r.weight)
		if r.weight == 0 {
			continue
		}

		err := r.underlying.Start()
		if err != nil {
			return errors.Wrapf(err, "%s cannot be started", r.underlying.Name())
		}

		count++

		if c.config.HealthChecksEnabled {
			c.livenessCheckLoops.Add(1)
			go c.loopCheckLiveness(r)
		}
	}

	if totalWeight == 0 {
		return errors.New("total runners weight must be > 0")
	}

	c.logger.Info().Uint("count", count).Msg("underlying runners have been started")

	return nil
}

// loopCheckLiveness periodically sends liveness probes to the provided runner.
// If the runner does not respond, it's marked as dead and excluded from load balancing.
// When the runner passes a liveness probe, it's included in load balancing.
func (c *Coordinator) loopCheckLiveness(r *Runner) {
	defer c.livenessCheckLoops.Done()

	rlogger := c.logger.With().Str("underlying_runner", r.underlying.Name()).Logger()
	rlogger.Debug().Dur("retry_delay_ms", c.config.HealthCheckRetryDelay).Msg("liveness loop has been started")

	checkLiveness := func() {
		withTimeout, cancel := context.WithTimeout(c.ctx, DefaultLivenessCheckTimeout)
		defer cancel()

		status := r.underlying.Status(withTimeout)

		select {
		case <-c.ctx.Done():
			return
		default:
		}

		if status.Alive {
			r.setAlive(true)
			c.balancer.add(r)

			return
		}

		r.setAlive(false)
		c.balancer.remove(r)

		rlogger.Debug().Err(status.LivenessProbeErr).Msg("runner is not responding")
	}

	checkLiveness()

	t := time.NewTicker(c.config.HealthCheckRetryDelay)
	defer t.Stop()

	for {
		select {
		case <-c.ctx.Done():
			rlogger.Debug().Msg("liveness loop has been stopped")
			return

		case <-t.C:
		}

		checkLiveness()
	}
}

// Stop stops underlying runners and waits for the health checks to be finished.
func (c *Coordinator) Stop(shutdownCtx context.Context) error {
	c.cancel()

	c.logger.Info().Msg("stopping coordinator")

	for _, r := range c.runners {
		if r.weight == 0 {
			continue
		}

		err := r.underlying.Stop(shutdownCtx)
		if err != nil {
			c.logger.Err(err).Str("underlying", r.underlying.Name()).Msg("runner cannot be stopped")
		}
	}

	c.logger.Info().Msg("runners have been stopped")

	c.livenessCheckLoops.Wait()

	c.logger.Info().Msg("coordinator has been stopped")

	return nil
}

// RunQuery proxies queries to one of the underlying runners.
func (c *Coordinator) RunQuery(ctx context.Context, run *queryrun.Run) (output string, err error) {
	processed := c.balancer.processJob(func(r *Runner) {
		output, err = r.underlying.RunQuery(ctx, run)
	})
	if !processed {
		return "", qrunner.ErrNoAvailableRunners
	}

	return output, err
}

// EnsureImage fans out to every alive runner and aggregates their statuses. Because a query
// may be dispatched to any alive runner, the image is only considered ready once all alive
// runners report it ready. If any runner is still building, the result is building; otherwise
// the first failure (or error) is returned.
func (c *Coordinator) EnsureImage(ctx context.Context, version string, bt buildtype.BuildType) (qrunner.ImageStatus, error) {
	if bt.IsRelease() {
		return qrunner.ImageStatus{State: qrunner.ImageReady}, nil
	}

	var (
		alive    int
		building *qrunner.ImageStatus
		failed   *qrunner.ImageStatus
		firstErr error
	)

	// TODO: Consider the image ready once any runner has built it; route each
	// request to a runner where the image is available.
	for _, r := range c.runners {
		if r.weight == 0 || !r.IsAlive() {
			continue
		}
		alive++

		status, err := r.underlying.EnsureImage(ctx, version, bt)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		switch status.State {
		case qrunner.ImageBuilding:
			s := status
			building = &s
		case qrunner.ImageFailed:
			s := status
			failed = &s
		case qrunner.ImageReady:
		}
	}

	switch {
	case alive == 0:
		if firstErr != nil {
			return qrunner.ImageStatus{}, firstErr
		}
		return qrunner.ImageStatus{}, qrunner.ErrNoAvailableRunners
	case building != nil:
		return *building, nil
	case failed != nil:
		return *failed, nil
	case firstErr != nil:
		return qrunner.ImageStatus{}, firstErr
	default:
		return qrunner.ImageStatus{State: qrunner.ImageReady}, nil
	}
}
