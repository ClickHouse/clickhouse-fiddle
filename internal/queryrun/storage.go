package queryrun

import (
	"bytes"
	"compress/gzip"
	"io"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/pkg/errors"
)

// maxItemSize is the largest DynamoDB item this package writes. The service limit is
// 400 KB (409,600 bytes); the rest is a margin for the size estimate.
const maxItemSize = 400_000

// prepareForStorage returns the run in its storage shape: the output is stored once, as
// gzipped exact bytes in OutputGz. The plain Output and RawOutput attributes are not
// written. The given run is not modified.
func prepareForStorage(run *Run) *Run {
	stored := *run
	stored.Output = ""
	stored.RawOutput = nil
	stored.RawOutputGz = nil

	raw := run.RawOutput
	if raw == nil {
		raw = []byte(run.Output)
	}
	if len(raw) > 0 {
		stored.OutputGz = gzipBytes(raw)
	}

	return &stored
}

// dropOutput returns the stored shape of a run whose output does not fit the item size
// limit: everything but the output is kept, and OutputDropped marks the loss.
func dropOutput(stored *Run) *Run {
	dropped := *stored
	dropped.OutputGz = nil
	dropped.OutputDropped = true

	return &dropped
}

// restoreFromStorage fills Output and RawOutput from whichever attributes the stored
// record has. Records come in several shapes, from oldest to newest:
//   - Output only (text): RawOutput stays nil, readers fall back to the Output bytes.
//   - Output and RawOutput (plain text and exact bytes): returned as is.
//   - OutputGz, optionally RawOutputGz (gzipped by an intermediate version): inflated.
//   - OutputGz only, or OutputDropped with no output at all: the current shape.
func restoreFromStorage(run *Run) error {
	if len(run.OutputGz) > 0 {
		raw, err := gunzipBytes(run.OutputGz)
		if err != nil {
			return errors.Wrap(err, "failed to decompress output")
		}

		run.Output = string(raw)
		run.RawOutput = raw
		run.OutputGz = nil
	}

	if len(run.RawOutputGz) > 0 {
		raw, err := gunzipBytes(run.RawOutputGz)
		if err != nil {
			return errors.Wrap(err, "failed to decompress raw output")
		}

		run.RawOutput = raw
		run.RawOutputGz = nil
	}

	return nil
}

// itemSize estimates the size DynamoDB charges for an item, following the documented
// rules: the UTF-8 length of each attribute name plus the size of its value.
func itemSize(item map[string]types.AttributeValue) int {
	size := 0
	for name, value := range item {
		size += len(name) + attributeSize(value)
	}

	return size
}

func attributeSize(value types.AttributeValue) int {
	const (
		nestedOverhead = 3 // Charged for each list or map.
		scalarSize     = 1 // Booleans and nulls.
	)

	switch v := value.(type) {
	case *types.AttributeValueMemberS:
		return len(v.Value)
	case *types.AttributeValueMemberB:
		return len(v.Value)
	case *types.AttributeValueMemberN:
		return numberSize(v.Value)
	case *types.AttributeValueMemberBOOL, *types.AttributeValueMemberNULL:
		return scalarSize
	case *types.AttributeValueMemberM:
		return nestedOverhead + itemSize(v.Value)
	case *types.AttributeValueMemberL:
		size := nestedOverhead
		for _, element := range v.Value {
			size += attributeSize(element)
		}

		return size
	case *types.AttributeValueMemberSS:
		size := 0
		for _, s := range v.Value {
			size += len(s)
		}

		return size
	case *types.AttributeValueMemberNS:
		size := 0
		for _, n := range v.Value {
			size += numberSize(n)
		}

		return size
	case *types.AttributeValueMemberBS:
		size := 0
		for _, b := range v.Value {
			size += len(b)
		}

		return size
	default:
		return 0
	}
}

// numberSize follows the DynamoDB rule: two significant digits per byte, plus one byte.
func numberSize(digits string) int {
	return (len(digits)+1)/2 + 1
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
