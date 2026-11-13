package queryrun

import (
	"bytes"
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
	// The byte-identical RawOutput is deduplicated instead of being compressed twice.
	require.Empty(t, stored.RawOutputGz)
	require.LessOrEqual(t, len(stored.OutputGz), storedOutputBudget)

	require.NoError(t, restoreFromStorage(stored))
	require.Equal(t, output, stored.Output)
	require.Empty(t, stored.RawOutput)
	require.Empty(t, stored.OutputGz)

	// The original run is left intact.
	require.Equal(t, output, run.Output)
	require.Equal(t, []byte(output), run.RawOutput)
	require.Empty(t, run.OutputGz)
}

func TestPrepareForStorageKeepsDistinctRawOutput(t *testing.T) {
	output := strings.Repeat("│ SHOW DATABASES │\n", 10000)
	raw := pseudoRandomBytes(64 << 10)
	run := &Run{ID: "id", Output: output, RawOutput: raw}

	stored := prepareForStorage(run)

	require.NotEmpty(t, stored.RawOutputGz)

	require.NoError(t, restoreFromStorage(stored))
	require.Equal(t, output, stored.Output)
	require.Equal(t, raw, stored.RawOutput)
}

func TestPrepareForStorageTruncatesIncompressibleOutput(t *testing.T) {
	noise := pseudoRandomBytes(400 << 10)
	run := &Run{ID: "id", Output: string(noise), RawOutput: noise}

	stored := prepareForStorage(run)

	require.Empty(t, stored.RawOutputGz)
	require.LessOrEqual(t, len(stored.OutputGz), storedOutputBudget)

	require.NoError(t, restoreFromStorage(stored))
	require.True(t, strings.HasSuffix(stored.Output, truncationMarker))
	require.True(t, utf8.ValidString(stored.Output))
	require.Empty(t, stored.RawOutput)

	// The kept prefix is the original data and it uses the budget: the noise does not
	// compress at all, so the cut lands within two chunks of the budget.
	kept := strings.TrimSuffix(stored.Output, truncationMarker)
	require.Equal(t, string(noise[:len(kept)]), kept)
	require.Greater(t, len(kept), storedOutputBudget-2*truncationChunk)
}

func TestPrepareForStorageKeepsMoreOfLargerOutput(t *testing.T) {
	prev := 0
	for _, size := range []int{128 << 10, 256 << 10, 512 << 10, 1 << 20} {
		noise := pseudoRandomBytes(size)
		stored := prepareForStorage(&Run{ID: "id", Output: string(noise), RawOutput: noise})

		require.LessOrEqual(t, len(stored.OutputGz), storedOutputBudget)
		require.NoError(t, restoreFromStorage(stored))
		// A larger output must never be stored shorter than a smaller one.
		require.GreaterOrEqual(t, len(stored.Output), prev, "size %d", size)
		prev = len(stored.Output)
	}
}

func TestPrepareForStorageMarksOnlyTruncatedFields(t *testing.T) {
	// Only the distinct RawOutput exceeds the budget; Output is tiny and must stay as is.
	raw := pseudoRandomBytes(400 << 10)
	run := &Run{ID: "id", Output: "1\n", RawOutput: raw}

	stored := prepareForStorage(run)

	require.LessOrEqual(t, len(stored.OutputGz)+len(stored.RawOutputGz), storedOutputBudget)

	require.NoError(t, restoreFromStorage(stored))
	require.Equal(t, "1\n", stored.Output)
	require.Less(t, len(stored.RawOutput), len(raw))
	require.Equal(t, raw[:len(stored.RawOutput)], stored.RawOutput)
	// RawOutput gets half of the budget and fills it to within two chunks.
	require.Greater(t, len(stored.RawOutput), storedOutputBudget/2-2*truncationChunk)
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

func TestAlignToRuneStartKeepsUTF8Valid(t *testing.T) {
	s := []byte(strings.Repeat("│", 100)) // 3 bytes per rune

	for limit := 1; limit < 10; limit++ {
		cut := alignToRuneStart(s, limit)
		require.LessOrEqual(t, cut, limit)
		require.True(t, utf8.Valid(s[:cut]))
	}

	// Binary data without rune starts loses at most three bytes (the longest rune tail).
	cont := bytes.Repeat([]byte{0x80}, 100)
	require.Equal(t, 47, alignToRuneStart(cont, 50))
	// A cut at the end does not move.
	require.Equal(t, len(s), alignToRuneStart(s, len(s)))
}

func TestRestoreFromStoragePlainRunIsUntouched(t *testing.T) {
	run := &Run{ID: "id", Output: "plain", RawOutput: []byte("plain")}

	require.NoError(t, restoreFromStorage(run))
	require.Equal(t, "plain", run.Output)
	require.Equal(t, []byte("plain"), run.RawOutput)
}
