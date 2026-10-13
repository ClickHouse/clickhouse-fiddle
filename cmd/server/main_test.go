package main

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewRouterOptsUsesIndependentLimits(t *testing.T) {
	opts := newRouterOpts(
		&Config{
			Limits: Limits{
				MaxQueryLength:  3,
				MaxOutputLength: 5,
			},
		},
		zerolog.Nop(),
		nil,
		nil,
		nil,
		nil,
	)

	require.Equal(t, uint64(3), opts.MaxQueryLength)
	require.Equal(t, uint64(5), opts.MaxOutputLength)
}
