package coordinator

import (
	"sync/atomic"

	"github.com/lodthe/clickhouse-playground/internal/qrunner"
)

const DefaultWeight = 100

type Runner struct {
	underlying qrunner.Runner

	// Liveness is controlled by ping probes.
	alive uint32

	// Weight is for load balancing.
	weight uint

	maxConcurrency *uint32
	concurrency    atomic.Int32
}

func NewRunner(underlying qrunner.Runner, weight uint, maxConcurrency *uint32) *Runner {
	return &Runner{
		underlying:     underlying,
		weight:         weight,
		alive:          0,
		maxConcurrency: maxConcurrency,
	}
}

func (r *Runner) IsAlive() bool {
	return atomic.LoadUint32(&r.alive) == 1
}

func (r *Runner) setAlive(alive bool) {
	var converted uint32
	if alive {
		converted = 1
	}

	atomic.StoreUint32(&r.alive, converted)
}

// addConcurrency atomically adds delta to the current concurrency and returns the new value.
func (r *Runner) addConcurrency(delta int32) uint32 {
	return uint32(r.concurrency.Add(delta))
}
