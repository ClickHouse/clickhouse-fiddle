package dockerengine

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutputLimitPassesSmallWrites(t *testing.T) {
	limit := newOutputLimit(10)
	var out, errOut bytes.Buffer

	n, err := limit.writer(&out).Write([]byte("1234"))
	require.NoError(t, err)
	require.Equal(t, 4, n)

	n, err = limit.writer(&errOut).Write([]byte("5678"))
	require.NoError(t, err)
	require.Equal(t, 4, n)

	require.False(t, limit.reached())
	require.Equal(t, "1234", out.String())
	require.Equal(t, "5678", errOut.String())
}

func TestOutputLimitCutsTheWriteThatCrossesTheBudget(t *testing.T) {
	limit := newOutputLimit(10)
	var out, errOut bytes.Buffer

	_, err := limit.writer(&out).Write([]byte("123456"))
	require.NoError(t, err)

	// stdout and stderr share the budget: 6 bytes are spent, 4 remain.
	n, err := limit.writer(&errOut).Write([]byte("abcdefgh"))
	require.ErrorIs(t, err, errOutputLimitReached)
	require.Equal(t, 4, n)

	require.True(t, limit.reached())
	require.Equal(t, "123456", out.String())
	require.Equal(t, "abcd", errOut.String())
}

func TestOutputLimitExactFitIsNotTruncated(t *testing.T) {
	limit := newOutputLimit(5)
	var out bytes.Buffer

	_, err := limit.writer(&out).Write([]byte("12345"))
	require.NoError(t, err)
	require.False(t, limit.reached())

	// The next byte does not fit.
	n, err := limit.writer(&out).Write([]byte("6"))
	require.ErrorIs(t, err, errOutputLimitReached)
	require.Equal(t, 0, n)
	require.True(t, limit.reached())
	require.Equal(t, "12345", out.String())
}

func TestOutputLimitZeroMeansUnlimited(t *testing.T) {
	limit := newOutputLimit(0)
	var out bytes.Buffer

	_, err := limit.writer(&out).Write([]byte(strings.Repeat("x", 1<<20)))
	require.NoError(t, err)
	require.False(t, limit.reached())
	require.Equal(t, 1<<20, out.Len())
}
