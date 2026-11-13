package queryrun

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"
)

func TestPrepareForStorageWritesOneGzippedCopy(t *testing.T) {
	raw := []byte{0xff, '\\', '0', '\n'} // Not valid UTF-8: only the bytes are exact.
	run := &Run{ID: "id", Input: "SELECT unhex('FF00')", Output: string(raw), RawOutput: raw}

	stored := prepareForStorage(run)

	require.Empty(t, stored.Output)
	require.Empty(t, stored.RawOutput)
	require.Empty(t, stored.RawOutputGz)
	require.NotEmpty(t, stored.OutputGz)
	require.False(t, stored.OutputDropped)

	item, err := attributevalue.MarshalMap(stored)
	require.NoError(t, err)
	_, hasOutput := item["Output"]
	require.False(t, hasOutput, "the plain text copy must not be written")
	_, hasRaw := item["RawOutput"]
	require.False(t, hasRaw, "the plain bytes copy must not be written")
	require.IsType(t, &types.AttributeValueMemberB{}, item["OutputGz"])

	require.NoError(t, restoreFromStorage(stored))
	require.Equal(t, raw, stored.RawOutput)
	require.Equal(t, string(raw), stored.Output)
	require.Empty(t, stored.OutputGz)

	// The original run is left intact.
	require.Equal(t, string(raw), run.Output)
	require.Equal(t, raw, run.RawOutput)
	require.Empty(t, run.OutputGz)
}

func TestPrepareForStorageUsesOutputWhenRawOutputIsAbsent(t *testing.T) {
	run := &Run{ID: "id", Output: "1\n"}

	stored := prepareForStorage(run)
	require.NotEmpty(t, stored.OutputGz)

	require.NoError(t, restoreFromStorage(stored))
	require.Equal(t, "1\n", stored.Output)
	require.Equal(t, []byte("1\n"), stored.RawOutput)
}

func TestPrepareForStorageEmptyOutputHasNoOutputAttribute(t *testing.T) {
	stored := prepareForStorage(&Run{ID: "id"})

	require.Empty(t, stored.OutputGz)
	require.NoError(t, restoreFromStorage(stored))
	require.Empty(t, stored.Output)
}

func TestDropOutputKeepsEverythingElse(t *testing.T) {
	run := &Run{ID: "id", Input: "SELECT 1", Version: "latest", Output: "1\n", OutputTruncated: true}

	dropped := dropOutput(prepareForStorage(run))

	require.True(t, dropped.OutputDropped)
	require.True(t, dropped.OutputTruncated)
	require.Empty(t, dropped.OutputGz)
	require.Equal(t, "SELECT 1", dropped.Input)
	require.Equal(t, "latest", dropped.Version)

	require.NoError(t, restoreFromStorage(dropped))
	require.Empty(t, dropped.Output)
	require.Empty(t, dropped.RawOutput)
}

// Records written by earlier versions must still be readable.
func TestRestoreFromStorageLegacyShapes(t *testing.T) {
	t.Run("text only, before RawOutput existed", func(t *testing.T) {
		run := &Run{ID: "id", Output: "plain"}

		require.NoError(t, restoreFromStorage(run))
		require.Equal(t, "plain", run.Output)
		require.Nil(t, run.RawOutput, "readers fall back to the Output bytes")
	})

	t.Run("text and exact bytes", func(t *testing.T) {
		run := &Run{ID: "id", Output: "plain", RawOutput: []byte("plain")}

		require.NoError(t, restoreFromStorage(run))
		require.Equal(t, "plain", run.Output)
		require.Equal(t, []byte("plain"), run.RawOutput)
	})

	t.Run("gzipped text with a distinct gzipped raw copy", func(t *testing.T) {
		run := &Run{ID: "id", OutputGz: gzipBytes([]byte("text")), RawOutputGz: gzipBytes([]byte{0xff})}

		require.NoError(t, restoreFromStorage(run))
		require.Equal(t, "text", run.Output)
		require.Equal(t, []byte{0xff}, run.RawOutput)
		require.Empty(t, run.OutputGz)
		require.Empty(t, run.RawOutputGz)
	})

	t.Run("corrupted gzip is an error", func(t *testing.T) {
		run := &Run{ID: "id", OutputGz: []byte("not gzip")}

		require.Error(t, restoreFromStorage(run))
	})
}

func TestItemSizeFollowsDynamoDBRules(t *testing.T) {
	item := map[string]types.AttributeValue{
		"Id":    &types.AttributeValueMemberS{Value: "abc"},                                                                           // 2 + 3
		"Gz":    &types.AttributeValueMemberB{Value: []byte{1, 2, 3, 4}},                                                              // 2 + 4
		"N":     &types.AttributeValueMemberN{Value: "12345"},                                                                         // 1 + (3 + 1)
		"Flag":  &types.AttributeValueMemberBOOL{Value: true},                                                                         // 4 + 1
		"Null":  &types.AttributeValueMemberNULL{Value: true},                                                                         // 4 + 1
		"M":     &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"k": &types.AttributeValueMemberS{Value: "vv"}}}, // 1 + 3 + (1 + 2)
		"L":     &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: "x"}}},                // 1 + 3 + 1
		"SS":    &types.AttributeValueMemberSS{Value: []string{"a", "bc"}},                                                            // 2 + 3
		"NS":    &types.AttributeValueMemberNS{Value: []string{"1", "22"}},                                                            // 2 + (2 + 2)
		"BS":    &types.AttributeValueMemberBS{Value: [][]byte{{1}, {2, 3}}},                                                          // 2 + 3
		"Other": nil,                                                                                                                  // 5 + 0
	}

	require.Equal(t, 5+6+5+5+5+7+5+5+6+5+5, itemSize(item))
}

func TestItemSizeOfARunIsCloseToItsPayload(t *testing.T) {
	output := strings.Repeat("x", 100_000)
	run := &Run{ID: "id", Input: "SELECT 1", Output: output, RawOutput: []byte(output)}

	item, err := attributevalue.MarshalMap(prepareForStorage(run))
	require.NoError(t, err)

	// 100 KB of one letter compresses to a few hundred bytes; the rest is metadata.
	require.Less(t, itemSize(item), 2_000)

	// Bytes that do not compress dominate the size one-to-one.
	noise := make([]byte, 300_000)
	_, err = rand.Read(noise)
	require.NoError(t, err)
	run = &Run{ID: "id", Input: "SELECT 1", Output: string(noise), RawOutput: noise}

	item, err = attributevalue.MarshalMap(prepareForStorage(run))
	require.NoError(t, err)
	require.Greater(t, itemSize(item), 250_000)
	require.Less(t, itemSize(item), 310_000)
}
