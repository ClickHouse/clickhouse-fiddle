package queryrun

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// pseudoRandomBytes returns deterministic ASCII noise that gzip cannot compress well.
func pseudoRandomBytes(n int) []byte {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

	data := make([]byte, n)
	state := uint64(42)
	for i := range data {
		state = state*6364136223846793005 + 1442695040888963407
		data[i] = alphabet[state>>58]
	}

	return data
}

func TestPrepareForStorageSmallRunIsUntouched(t *testing.T) {
	run := &Run{ID: "id", Output: "1\n", RawOutput: []byte("1\n")}

	stored := prepareForStorage(run)

	require.Same(t, run, stored)
	require.Empty(t, stored.OutputGz)
	require.Empty(t, stored.RawOutputGz)
}

func TestPrepareForStorageCompressesLargeOutput(t *testing.T) {
	// Mimics Pretty-formatted output: large, heavily padded, well compressible.
	output := strings.Repeat("│ SHOW DATABASES      │ []        │ DATABASE │ SHOW │\n", 10000)
	run := &Run{ID: "id", Output: output, RawOutput: []byte(output)}

	stored := prepareForStorage(run)

	require.NotSame(t, run, stored)
	require.Empty(t, stored.Output)
	require.Empty(t, stored.RawOutput)
	require.LessOrEqual(t, len(stored.OutputGz)+len(stored.RawOutputGz), storedOutputBudget)

	require.NoError(t, restoreFromStorage(stored))
	require.Equal(t, output, stored.Output)
	require.Equal(t, []byte(output), stored.RawOutput)
	require.Empty(t, stored.OutputGz)
	require.Empty(t, stored.RawOutputGz)

	// The original run is left intact.
	require.Equal(t, output, run.Output)
	require.Empty(t, run.OutputGz)
}

func TestPrepareForStorageTruncatesIncompressibleOutput(t *testing.T) {
	noise := pseudoRandomBytes(400 << 10)
	run := &Run{ID: "id", Output: string(noise), RawOutput: noise}

	stored := prepareForStorage(run)

	require.LessOrEqual(t, len(stored.OutputGz)+len(stored.RawOutputGz), storedOutputBudget)

	require.NoError(t, restoreFromStorage(stored))
	require.True(t, strings.HasSuffix(stored.Output, truncationMarker))
	require.LessOrEqual(t, len(stored.Output), truncatedOutputCap+len(truncationMarker))
	require.True(t, utf8.ValidString(stored.Output))
	require.Len(t, stored.RawOutput, truncatedOutputCap)
	require.Equal(t, noise[:truncatedOutputCap], stored.RawOutput)
}

func TestPrepareForStorageWithoutRawOutput(t *testing.T) {
	run := &Run{ID: "id", Output: strings.Repeat("x", compressThreshold)}

	stored := prepareForStorage(run)

	require.NotEmpty(t, stored.OutputGz)
	require.Empty(t, stored.RawOutputGz)

	require.NoError(t, restoreFromStorage(stored))
	require.Equal(t, run.Output, stored.Output)
	require.Empty(t, stored.RawOutput)
}

func TestTruncateStringKeepsUTF8Valid(t *testing.T) {
	s := strings.Repeat("│", 100) // 3 bytes per rune

	for limit := 1; limit < 10; limit++ {
		truncated := truncateString(s, limit)
		require.LessOrEqual(t, len(truncated), limit)
		require.True(t, utf8.ValidString(truncated))
	}
}

func TestRestoreFromStoragePlainRunIsUntouched(t *testing.T) {
	run := &Run{ID: "id", Output: "plain", RawOutput: []byte("plain")}

	require.NoError(t, restoreFromStorage(run))
	require.Equal(t, "plain", run.Output)
	require.Equal(t, []byte("plain"), run.RawOutput)
}
