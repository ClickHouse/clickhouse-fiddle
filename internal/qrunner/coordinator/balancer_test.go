package coordinator

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lodthe/clickhouse-playground/internal/qrunner/stubrunner"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

func TestBalancer_processJob_ConcurrencyLimitExhausted(t *testing.T) {
	ctx := context.Background()
	maxConcurrency := uint32(5)

	r1 := NewRunner(stubrunner.New(ctx, "runner_1", stubrunner.StubRun), 100, &maxConcurrency)
	r2 := NewRunner(stubrunner.New(ctx, "runner_2", stubrunner.StubRun), 300, &maxConcurrency)

	b := newBalancer(zlog.Logger.Level(zerolog.ErrorLevel))
	assert.True(t, b.add(r1))
	assert.True(t, b.add(r2))

	const iterations = 1000

	for range iterations {
		jobsCreated := sync.WaitGroup{}
		jobsCompleted := sync.WaitGroup{}
		initFinished, finishInit := context.WithCancel(context.Background())

		r1Selected := new(uint32)
		r2Selected := new(uint32)

		for j := uint32(0); j < *r1.maxConcurrency+*r2.maxConcurrency; j++ {
			jobsCreated.Add(1)

			jobsCompleted.Go(func() {
				processed := b.processJob(func(r *Runner) {
					jobsCreated.Done()
					<-initFinished.Done()

					switch r {
					case r1:
						atomic.AddUint32(r1Selected, 1)
					case r2:
						atomic.AddUint32(r2Selected, 1)
					default:
						panic("unknown runner")
					}
				})

				assert.True(t, processed)
			})
		}

		jobsCreated.Wait()

		for range 10 {
			processed := b.processJob(func(r *Runner) {})
			assert.False(t, processed)
		}

		finishInit()
		jobsCompleted.Wait()

		assert.Equal(t, *r1.maxConcurrency, *r1Selected)
		assert.Equal(t, *r2.maxConcurrency, *r2Selected)
	}
}

func TestBalancer_selectRunner_EqualWeights(t *testing.T) {
	const runnerCount = 5
	const samples = 10000
	const maxDeviation = 0.1
	const expected = samples / runnerCount
	// Each runner should be selected samples / runnerCount times roughly.

	ctx := context.Background()
	b := newBalancer(zlog.Logger)

	var runners []*Runner
	for i := range runnerCount {
		r := NewRunner(stubrunner.New(ctx, fmt.Sprintf("r%d", i), stubrunner.StubRun), 100, nil)

		assert.True(t, b.add(r))
		runners = append(runners, r)
	}

	timesSelected := make(map[*Runner]uint, len(runners))
	for range samples {
		r := b.selectRunner()
		timesSelected[r]++
	}

	for _, r := range runners {
		count := timesSelected[r]

		deviation := math.Abs(float64(count)/expected - 1)
		assert.LessOrEqual(t, deviation, maxDeviation)
	}
}

func TestBalancer_selectRunner_DifferentWeights(t *testing.T) {
	const runnerCount = 5
	const samples = 20000
	const maxDeviation = 0.2

	var runners []*Runner
	var totalWeight float64

	ctx := context.Background()
	b := newBalancer(zlog.Logger)

	// The weight of the i-th runner is (i + 1) * 100.
	for i := range runnerCount {
		r := NewRunner(stubrunner.New(ctx, fmt.Sprintf("r%d", i), stubrunner.StubRun), 100*uint(i+1), nil)
		runners = append(runners, r)
		assert.True(t, b.add(r))
		totalWeight += float64(r.weight)
	}

	timesSelected := make(map[*Runner]uint, len(runners))
	for range samples {
		r := b.selectRunner()
		timesSelected[r]++
	}

	for i, r := range runners {
		count := timesSelected[r]
		expected := samples * (float64(i+1) * 100 / totalWeight)

		deviation := math.Abs(float64(count)/expected - 1)
		assert.LessOrEqual(t, deviation, maxDeviation)
	}
}
