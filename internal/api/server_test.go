package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/cpuwork"
	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/queue"
)

type fakeQueue struct {
	mu       sync.Mutex
	payloads []string
	err      error
	stats    queue.Stats
}

func (f *fakeQueue) Enqueue(_ context.Context, p string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.payloads = append(f.payloads, p)
	return "1-1", nil
}

func (f *fakeQueue) Stats(_ context.Context) (queue.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats, f.err
}

func newServer(q Queue) *Server {
	return New(Config{Pod: "api-0", WorkIterations: 10, MaxPayloadBytes: 16}, q, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWork(t *testing.T) {
	h := newServer(&fakeQueue{}).Handler()
	rec := do(t, h, http.MethodGet, "/api/v1/work", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["pod"] != "api-0" || body["digest"] != cpuwork.Burn(10) {
		t.Fatalf("body = %v", body)
	}
}

func TestEnqueue(t *testing.T) {
	q := &fakeQueue{}
	h := newServer(q).Handler()

	if rec := do(t, h, http.MethodPost, "/api/v1/jobs", `{"n":1}`); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/jobs", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("empty body status = %d", rec.Code)
	}
	if got := strings.Join(q.payloads, ","); got != `{"n":1},{}` {
		t.Fatalf("payloads = %s", got)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/jobs", strings.Repeat("x", 17)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/jobs", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/v1/jobs status = %d", rec.Code)
	}

	q.err = errors.New("down")
	if rec := do(t, h, http.MethodPost, "/api/v1/jobs", "{}"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("queue down status = %d", rec.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestEnqueueBodyReadError(t *testing.T) {
	h := newServer(&fakeQueue{}).Handler()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/jobs", errReader{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestQueueStats(t *testing.T) {
	q := &fakeQueue{stats: queue.Stats{Stream: "jobs", Lag: 5, Pending: 2}}
	h := newServer(q).Handler()
	rec := do(t, h, http.MethodGet, "/api/v1/queue", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var st queue.Stats
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Lag != 5 || st.Pending != 2 {
		t.Fatalf("stats = %+v", st)
	}
	q.err = errors.New("down")
	if rec := do(t, h, http.MethodGet, "/api/v1/queue", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("queue down status = %d", rec.Code)
	}
}

func TestProbes(t *testing.T) {
	s := newServer(&fakeQueue{})
	h := s.Handler()
	if rec := do(t, h, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before ready = %d", rec.Code)
	}
	s.SetReady(true)
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusOK {
		t.Fatalf("readyz when ready = %d", rec.Code)
	}
	s.SetReady(false)
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining = %d", rec.Code)
	}
}

func TestMetricsAndQueueWatch(t *testing.T) {
	q := &fakeQueue{stats: queue.Stats{Lag: 7, Pending: 3, Processed: 11}}
	s := newServer(q)
	h := s.Handler()
	do(t, h, http.MethodGet, "/api/v1/work", "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.WatchQueue(ctx, 5*time.Millisecond)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	rec := do(t, h, http.MethodGet, "/metrics", "")
	out := rec.Body.String()
	for _, want := range []string{
		`http_requests_total{code="200",method="GET",route="/api/v1/work"} 1`,
		`http_request_duration_seconds_bucket{code="200",route="/api/v1/work",le="0.25"} 1`,
		"jobs_queue_lag 7",
		"jobs_queue_pending 3",
		"jobs_processed 11",
		"jobs_queue_up 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q", want)
		}
	}

	q.err = errors.New("down")
	s.refreshQueue(context.Background())
	if out := do(t, h, http.MethodGet, "/metrics", "").Body.String(); !strings.Contains(out, "jobs_queue_up 0") {
		t.Error("jobs_queue_up should be 0 after a failed read")
	}
}

func TestNewDefaults(t *testing.T) {
	s := New(Config{WorkIterations: -1}, &fakeQueue{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s.cfg.MaxPayloadBytes != 4096 || s.cfg.WorkIterations != 0 {
		t.Fatalf("defaults = %+v", s.cfg)
	}
}
