package restapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lodthe/clickhouse-playground/internal/dockertag"
	"github.com/lodthe/clickhouse-playground/internal/qrunner"
	"github.com/lodthe/clickhouse-playground/internal/qrunner/stubrunner"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

type anyTagStorage struct{}

func (anyTagStorage) GetAll() []dockertag.Image { return nil }
func (anyTagStorage) Exists(string) bool        { return true }

// memoryRepo mimics the DynamoDB repository: runs whose output is longer than maxOutput
// are stored without it and flagged, like Repo.Create does for oversized items.
type memoryRepo struct {
	maxOutput int
	runs      map[string]queryrun.Run
}

func (m *memoryRepo) Create(run *queryrun.Run) error {
	stored := *run
	if len(stored.Output) > m.maxOutput {
		stored.Output = ""
		stored.RawOutput = nil
		stored.OutputDropped = true
		run.OutputDropped = true
	}
	m.runs[run.ID] = stored

	return nil
}

func (m *memoryRepo) Get(id string) (*queryrun.Run, error) {
	run, ok := m.runs[id]
	if !ok {
		return nil, queryrun.ErrNotFound
	}

	return &run, nil
}

func newTestRouter(t *testing.T, output qrunner.QueryOutput, maxStoredOutput int) http.Handler {
	t.Helper()

	runner := stubrunner.New(context.Background(), "stub", func(context.Context, *queryrun.Run) (qrunner.QueryOutput, error) {
		return output, nil
	})
	repo := &memoryRepo{maxOutput: maxStoredOutput, runs: map[string]queryrun.Run{}}

	r := chi.NewRouter()
	newQueryHandler(runner, repo, anyTagStorage{}, 1000).handle(r)

	return r
}

func postRun(t *testing.T, router http.Handler, query string) map[string]any {
	t.Helper()

	body := strings.NewReader(`{"query": ` + strconvQuote(query) + `, "version": "latest", "include_raw_output": true}`)
	req := httptest.NewRequest(http.MethodPost, "/runs", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	return decodeResult(t, w)
}

func getRun(t *testing.T, router http.Handler, id string) map[string]any {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/runs/"+id+"?include_raw_output=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	return decodeResult(t, w)
}

func decodeResult(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var resp struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	return resp.Result
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestRunQueryReturnsTheWholeOutputAndTheFlags(t *testing.T) {
	// The runner cut the output at its cap, and the output is too large for the storage.
	router := newTestRouter(t, qrunner.QueryOutput{Output: "0123456789", Truncated: true}, 5)

	posted := postRun(t, router, "SELECT 1")
	require.Equal(t, "0123456789", posted["output"], "the response carries the full output that was read")
	require.Equal(t, true, posted["output_truncated"])
	require.Equal(t, true, posted["output_dropped"])

	got := getRun(t, router, posted["query_run_id"].(string))
	require.Equal(t, "", got["output"], "a dropped output is not available from storage")
	require.Equal(t, true, got["output_dropped"])
	require.Equal(t, true, got["output_truncated"])
	require.Equal(t, "SELECT 1", got["input"])
}

func TestRunQueryOmitsTheFlagsWhenNothingWasLost(t *testing.T) {
	router := newTestRouter(t, qrunner.QueryOutput{Output: "1\n"}, 1000)

	posted := postRun(t, router, "SELECT 1")
	require.Equal(t, "1\n", posted["output"])
	_, truncated := posted["output_truncated"]
	require.False(t, truncated)
	_, dropped := posted["output_dropped"]
	require.False(t, dropped)

	got := getRun(t, router, posted["query_run_id"].(string))
	require.Equal(t, "1\n", got["output"])
	_, dropped = got["output_dropped"]
	require.False(t, dropped)
}

func TestRunQueryStillRejectsAQueryAboveTheCap(t *testing.T) {
	router := newTestRouter(t, qrunner.QueryOutput{Output: "1\n"}, 1000)

	body := strings.NewReader(`{"query": "` + strings.Repeat("x", 1001) + `", "version": "latest"}`)
	req := httptest.NewRequest(http.MethodPost, "/runs", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "query length (1001) cannot exceed 1000")
}
