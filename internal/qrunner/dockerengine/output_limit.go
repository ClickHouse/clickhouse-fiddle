package dockerengine

import (
	"io"

	"github.com/pkg/errors"
)

// errOutputLimitReached is returned by a limited writer once the shared budget is spent.
// It stops stdcopy.StdCopy, which returns it to the caller.
var errOutputLimitReached = errors.New("output size limit reached")

// outputLimit is a byte budget shared by several writers (stdout and stderr of one
// command). Writes that fit are passed through; the write that crosses the budget is cut
// at the budget and fails with errOutputLimitReached. A zero budget means no limit.
type outputLimit struct {
	remaining uint64
	unlimited bool
	exhausted bool
}

func newOutputLimit(maxBytes uint64) *outputLimit {
	return &outputLimit{
		remaining: maxBytes,
		unlimited: maxBytes == 0,
	}
}

// reached reports whether a write was cut because the budget was spent.
func (l *outputLimit) reached() bool {
	return l.exhausted
}

// writer wraps dst so that its writes count against the shared budget. The writers are
// used from one goroutine (stdcopy.StdCopy demultiplexes into them sequentially).
func (l *outputLimit) writer(dst io.Writer) io.Writer {
	if l.unlimited {
		return dst
	}

	return &limitedWriter{limit: l, dst: dst}
}

type limitedWriter struct {
	limit *outputLimit
	dst   io.Writer
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	l := w.limit

	if uint64(len(p)) <= l.remaining {
		n, err := w.dst.Write(p)
		l.remaining -= uint64(n)

		return n, err
	}

	n, err := w.dst.Write(p[:l.remaining])
	l.remaining -= uint64(n)
	l.exhausted = true

	if err != nil {
		return n, err
	}

	return n, errOutputLimitReached
}
