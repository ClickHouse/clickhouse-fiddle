package restapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/require"
)

func TestWriteResultSetsJSONContentType(t *testing.T) {
	w := httptest.NewRecorder()

	writeResult(w, "ok")

	require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
}

func TestWriteErrorSetsJSONContentType(t *testing.T) {
	w := httptest.NewRecorder()

	writeError(w, "boom", http.StatusBadRequest)

	require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// Responses must carry a JSON content type before the first body write, otherwise
// the Compress middleware used by NewRouter never engages.
func TestResponsesAreGzippedWhenAccepted(t *testing.T) {
	r := chi.NewRouter()
	r.Use(middleware.Compress(5, "application/json"))
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		writeResult(w, strings.Repeat("padded output ", 10000))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	require.Equal(t, "gzip", w.Header().Get("Content-Encoding"))

	compressedLen := w.Body.Len()
	gz, err := gzip.NewReader(w.Body)
	require.NoError(t, err)
	body, err := io.ReadAll(gz)
	require.NoError(t, err)
	require.Contains(t, string(body), "padded output")
	require.Greater(t, len(body), compressedLen)
}
