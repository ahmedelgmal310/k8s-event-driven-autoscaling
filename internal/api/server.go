// Package api is the HTTP front end: a CPU-bound request endpoint that the
// API autoscaler is driven by, and a producer endpoint that turns requests
// into queued jobs for the workers.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/cpuwork"
	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/queue"
)

// Queue is the part of the job queue the API needs.
type Queue interface {
	Enqueue(ctx context.Context, payload string) (string, error)
	Stats(ctx context.Context) (queue.Stats, error)
}

// Config tunes the API.
type Config struct {
	// Pod is reported in responses so load tests can see the spread across replicas.
	Pod string
	// WorkIterations is the number of chained SHA-256 rounds per /api/v1/work
	// request. It sets the CPU cost of a request, which is what makes the CPU
	// trigger of the autoscaler meaningful.
	WorkIterations int
	// MaxPayloadBytes caps the size of a job payload.
	MaxPayloadBytes int64
}

// Server serves the API.
type Server struct {
	cfg   Config
	q     Queue
	log   *slog.Logger
	ready atomic.Bool
	reg   *prometheus.Registry

	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	inflight  prometheus.Gauge
	enqueued  prometheus.Counter
	queueLag  prometheus.Gauge
	queuePend prometheus.Gauge
	queueDone prometheus.Gauge
	queueUp   prometheus.Gauge
}

// New builds a Server with its own Prometheus registry. The server starts not ready.
func New(cfg Config, q Queue, log *slog.Logger) *Server {
	if cfg.MaxPayloadBytes <= 0 {
		cfg.MaxPayloadBytes = 4096
	}
	if cfg.WorkIterations < 0 {
		cfg.WorkIterations = 0
	}
	reg := prometheus.NewRegistry()
	s := &Server{
		cfg: cfg,
		q:   q,
		log: log,
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by route, method and status code.",
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by route and status code.",
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}, []string{"route", "code"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Requests currently being served.",
		}),
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jobs_enqueued_total",
			Help: "Jobs accepted and appended to the stream.",
		}),
		queueLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jobs_queue_lag",
			Help: "Entries not yet delivered to the worker consumer group (the KEDA scaling signal).",
		}),
		queuePend: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jobs_queue_pending",
			Help: "Entries delivered to a worker but not yet acknowledged.",
		}),
		queueDone: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jobs_processed",
			Help: "Jobs acknowledged by workers, read from the shared counter in Redis.",
		}),
		queueUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jobs_queue_up",
			Help: "1 when the last queue stats read succeeded.",
		}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		s.requests, s.duration, s.inflight, s.enqueued,
		s.queueLag, s.queuePend, s.queueDone, s.queueUp,
	)
	return s
}

// SetReady flips the readiness probe. It is set to false at the start of a
// graceful shutdown so the pod leaves the Service endpoints first.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/work", s.instrument("/api/v1/work", http.HandlerFunc(s.work)))
	mux.Handle("POST /api/v1/jobs", s.instrument("/api/v1/jobs", http.HandlerFunc(s.enqueue)))
	mux.Handle("GET /api/v1/queue", s.instrument("/api/v1/queue", http.HandlerFunc(s.queueStats)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{Registry: s.reg}))
	return mux
}

// WatchQueue refreshes the queue gauges every interval until ctx is done.
// The gauges back the backlog alerts; KEDA reads Redis directly.
func (s *Server) WatchQueue(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.refreshQueue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) refreshQueue(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := s.q.Stats(ctx)
	if err != nil {
		s.queueUp.Set(0)
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("queue stats", "err", err)
		}
		return
	}
	s.queueUp.Set(1)
	s.queueLag.Set(float64(st.Lag))
	s.queuePend.Set(float64(st.Pending))
	s.queueDone.Set(float64(st.Processed))
}

func (s *Server) work(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"pod":        s.cfg.Pod,
		"iterations": s.cfg.WorkIterations,
		"digest":     cpuwork.Burn(s.cfg.WorkIterations),
	})
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxPayloadBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot read body"})
		return
	}
	payload := string(body)
	if payload == "" {
		payload = "{}"
	}
	id, err := s.q.Enqueue(r.Context(), payload)
	if err != nil {
		s.log.Error("enqueue", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "queue unavailable"})
		return
	}
	s.enqueued.Inc()
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *Server) queueStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.q.Stats(r.Context())
	if err != nil {
		s.log.Error("queue stats", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "queue unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		s.inflight.Inc()
		defer s.inflight.Dec()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		code := strconv.Itoa(rec.code)
		s.requests.WithLabelValues(route, r.Method, code).Inc()
		s.duration.WithLabelValues(route, code).Observe(time.Since(start).Seconds())
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
