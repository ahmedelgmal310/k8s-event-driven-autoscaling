// Package worker consumes jobs from the stream. It is built to be scaled by
// KEDA, including down to zero: on shutdown it stops taking new jobs, finishes
// and acknowledges the ones in flight, and leaves the consumer group clean.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/cpuwork"
	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/queue"
)

// Queue is the part of the job queue a worker needs.
type Queue interface {
	Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]queue.Message, error)
	Reclaim(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]queue.Message, error)
	Ack(ctx context.Context, id string) error
	RemoveConsumer(ctx context.Context, consumer string) (bool, error)
}

// Config tunes a worker.
type Config struct {
	// Consumer is the consumer name in the group; the pod name in Kubernetes.
	Consumer string
	// Concurrency is the number of jobs processed in parallel.
	Concurrency int
	// ProcessTime simulates the I/O part of a job (a downstream call).
	ProcessTime time.Duration
	// WorkIterations is the CPU part of a job (see cpuwork.Burn).
	WorkIterations int
	// Block is how long one read waits for new jobs.
	Block time.Duration
	// ReclaimIdle is how long a job may stay unacknowledged before another
	// worker takes it over. ReclaimEvery is how often that check runs (0 disables it).
	ReclaimIdle  time.Duration
	ReclaimEvery time.Duration
	// AckTimeout bounds the acknowledgement of a finished job.
	AckTimeout time.Duration
}

// Worker processes jobs until its context is cancelled.
type Worker struct {
	cfg Config
	q   Queue
	log *slog.Logger

	processed *prometheus.CounterVec
	duration  prometheus.Histogram
	inflight  prometheus.Gauge
	errors    prometheus.Counter
	reclaimed prometheus.Counter
}

// New builds a worker and registers its metrics on reg.
func New(cfg Config, q Queue, log *slog.Logger, reg prometheus.Registerer) *Worker {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Block <= 0 {
		cfg.Block = 2 * time.Second
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = 5 * time.Second
	}
	w := &Worker{
		cfg: cfg,
		q:   q,
		log: log,
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "worker_jobs_processed_total",
			Help: "Jobs processed by result (ok, ack_error).",
		}, []string{"result"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "worker_job_duration_seconds",
			Help:    "Time to process and acknowledge one job.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "worker_jobs_in_flight",
			Help: "Jobs currently being processed.",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "worker_queue_errors_total",
			Help: "Failed reads from the queue.",
		}),
		reclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "worker_jobs_reclaimed_total",
			Help: "Jobs taken over from consumers that stopped without acknowledging them.",
		}),
	}
	reg.MustRegister(w.processed, w.duration, w.inflight, w.errors, w.reclaimed)
	return w
}

// Run processes jobs until ctx is cancelled, then drains: every job already
// read is finished and acknowledged before Run returns.
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range w.cfg.Concurrency {
		wg.Go(func() { w.loop(ctx) })
	}
	if w.cfg.ReclaimEvery > 0 {
		wg.Go(func() { w.reclaimLoop(ctx) })
	}
	wg.Wait()

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.AckTimeout)
	defer cancel()
	removed, err := w.q.RemoveConsumer(cleanup, w.cfg.Consumer)
	if err != nil {
		w.log.Warn("remove consumer", "consumer", w.cfg.Consumer, "err", err)
		return
	}
	w.log.Info("worker drained", "consumer", w.cfg.Consumer, "consumer_removed", removed)
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		msgs, err := w.q.Read(ctx, w.cfg.Consumer, 1, w.cfg.Block)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.errors.Inc()
			w.log.Warn("read", "err", err)
			sleep(ctx, time.Second)
			continue
		}
		for _, m := range msgs {
			w.handle(ctx, m)
		}
	}
}

func (w *Worker) reclaimLoop(ctx context.Context) {
	t := time.NewTicker(w.cfg.ReclaimEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		msgs, err := w.q.Reclaim(ctx, w.cfg.Consumer, w.cfg.ReclaimIdle, 10)
		if err != nil {
			if ctx.Err() == nil {
				w.errors.Inc()
				w.log.Warn("reclaim", "err", err)
			}
			continue
		}
		for _, m := range msgs {
			w.reclaimed.Inc()
			w.handle(ctx, m)
		}
	}
}

// handle processes one job to completion. It deliberately ignores
// cancellation of ctx (shutdown): a job that was read is finished and
// acknowledged, never abandoned.
func (w *Worker) handle(ctx context.Context, m queue.Message) {
	start := time.Now()
	w.inflight.Inc()
	defer w.inflight.Dec()

	cpuwork.Burn(w.cfg.WorkIterations)
	if w.cfg.ProcessTime > 0 {
		time.Sleep(w.cfg.ProcessTime)
	}

	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.AckTimeout)
	defer cancel()
	if err := w.q.Ack(ackCtx, m.ID); err != nil {
		w.processed.WithLabelValues("ack_error").Inc()
		w.log.Error("ack", "id", m.ID, "err", err)
		return
	}
	w.processed.WithLabelValues("ok").Inc()
	w.duration.Observe(time.Since(start).Seconds())
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
