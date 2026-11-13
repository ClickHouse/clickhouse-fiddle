package main

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewRouterOptsUsesTheQueryLimit(t *testing.T) {
	opts := newRouterOpts(
		&Config{
			Limits: Limits{
				MaxQueryLength: 3,
			},
		},
		zerolog.Nop(),
		nil,
		nil,
		nil,
		nil,
	)

	require.Equal(t, uint64(3), opts.MaxQueryLength)
}
