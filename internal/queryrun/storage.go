package queryrun

import (
	"bytes"
	"compress/gzip"
	"io"
	"unicode/utf8"

	"github.com/pkg/errors"
)

const (
	// Outputs whose combined size stays below this are stored as plain attributes.
	compressThreshold = 32 << 10

	// Combined budget for the stored output attributes, derived from the 400 KB
	// DynamoDB item limit with slack for the query input and the other attributes.
	storedOutputBudget = 256 << 10

	// Step at which an over-budget output is shortened while looking for the longest
	// prefix that fits. Smaller steps waste less of the budget but cost more passes.
	truncationChunk = 16 << 10

	// Reserved out of the budget for gzip framing (header, trailer, block headers)
	// when estimating how much of an output fits.
	truncationReserve = 256

	truncationMarker = "\n... (output truncated for storage)"
)

// prepareForStorage returns the run shaped to fit the DynamoDB item size limit:
// large outputs are stored gzipped, and truncated first if compression alone is
// not enough. The given run is not modified.
func prepareForStorage(run *Run) *Run {
	if len(run.Output)+len(run.RawOutput) < compressThreshold {
		return run
	}

	output := run.Output
	rawOutput := run.RawOutput

	// A byte-identical RawOutput is not stored: readers fall back to Output bytes.
	if string(rawOutput) == output {
		rawOutput = nil
	}

	outputGz := gzipBytes([]byte(output))

	var rawOutputGz []byte
	if len(rawOutput) > 0 {
		rawOutputGz = gzipBytes(rawOutput)
	}

	if len(outputGz)+len(rawOutputGz) > storedOutputBudget {
		// Incompressible output: keep the longest prefix of each field that fits.
		// Only a distinct RawOutput shares the budget; the common case gets all of it.
		fieldBudget := storedOutputBudget
		if len(rawOutput) > 0 {
			fieldBudget /= 2
		}

		if len(outputGz) > fieldBudget {
			_, outputGz = compressPrefix([]byte(output), []byte(truncationMarker), fieldBudget)
		}

		if len(rawOutputGz) > fieldBudget {
			var kept int
			kept, rawOutputGz = compressPrefix(rawOutput, nil, fieldBudget)
			rawOutput = rawOutput[:kept]
		}
	}

	stored := *run
	stored.Output = ""
	stored.OutputGz = outputGz
	stored.RawOutput = nil
	if len(rawOutput) > 0 {
		stored.RawOutputGz = rawOutputGz
	}

	return &stored
}

// restoreFromStorage inflates the gzipped output attributes back into their plain
// counterparts. Runs written before compression was introduced are returned as is.
func restoreFromStorage(run *Run) error {
	if len(run.OutputGz) > 0 {
		output, err := gunzipBytes(run.OutputGz)
		if err != nil {
			return errors.Wrap(err, "failed to decompress output")
		}

		run.Output = string(output)
		run.OutputGz = nil
	}

	if len(run.RawOutputGz) > 0 {
		rawOutput, err := gunzipBytes(run.RawOutputGz)
		if err != nil {
			return errors.Wrap(err, "failed to decompress raw output")
		}

		run.RawOutput = rawOutput
		run.RawOutputGz = nil
	}

	return nil
}

// compressPrefix gzips the longest prefix of data, followed by suffix, whose gzip
// form fits into budget bytes. It returns the prefix length and the gzip form.
// The prefix is cut at a rune boundary, so a truncated string stays valid UTF-8.
func compressPrefix(data, suffix []byte, budget int) (kept int, gz []byte) {
	kept = estimatePrefixLength(data, budget-len(suffix))

	for {
		kept = alignToRuneStart(data, kept)

		gz = gzipBytes(data[:kept], suffix)
		if len(gz) <= budget || kept == 0 {
			return kept, gz
		}

		// The estimate was optimistic by whatever deflate still buffered: step back.
		kept -= min(truncationChunk, kept)
	}
}

// estimatePrefixLength feeds data to gzip chunk by chunk and stops before the chunk
// that could exceed budget if it did not compress at all. Flush does not push out
// everything deflate buffers, so the result is an estimate that the caller verifies.
func estimatePrefixLength(data []byte, budget int) int {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)

	kept := 0
	for kept < len(data) && buf.Len()+truncationChunk+truncationReserve <= budget {
		end := min(kept+truncationChunk, len(data))
		_, _ = w.Write(data[kept:end])
		_ = w.Flush()
		kept = end
	}

	return kept
}

// alignToRuneStart moves the cut left, by at most the tail of one rune, so that it
// does not split a UTF-8 sequence. Binary data that has no rune starts loses at
// most utf8.UTFMax-1 bytes.
func alignToRuneStart(data []byte, cut int) int {
	for back := 0; back < utf8.UTFMax-1 && cut > 0 && cut < len(data) && !utf8.RuneStart(data[cut]); back++ {
		cut--
	}

	return cut
}

func gzipBytes(parts ...[]byte) []byte {
	var buf bytes.Buffer

	w := gzip.NewWriter(&buf)
	for _, part := range parts {
		_, _ = w.Write(part)
	}
	_ = w.Close()

	return buf.Bytes()
}

func gunzipBytes(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	return io.ReadAll(r)
}
