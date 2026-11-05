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

	// Per-field cap applied when even the compressed outputs do not fit the budget
	// (incompressible output). Gzip never expands input beyond a fraction of a
	// percent, so capped sources are guaranteed to fit without a fitting loop.
	truncatedOutputCap = 100 << 10

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

	outputGz := gzipBytes([]byte(output))
	rawOutputGz := gzipBytes(rawOutput)

	if len(outputGz)+len(rawOutputGz) > storedOutputBudget {
		output = truncateString(output, truncatedOutputCap) + truncationMarker
		outputGz = gzipBytes([]byte(output))

		if len(rawOutput) > truncatedOutputCap {
			rawOutput = rawOutput[:truncatedOutputCap]
		}
		rawOutputGz = gzipBytes(rawOutput)
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

// truncateString cuts the string at a rune boundary: DynamoDB rejects strings
// that are not valid UTF-8.
func truncateString(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}

	return s[:limit]
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer

	w := gzip.NewWriter(&buf)
	_, _ = w.Write(data)
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
