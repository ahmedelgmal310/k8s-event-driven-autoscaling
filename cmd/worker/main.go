// Command worker consumes jobs from the Redis Stream. KEDA scales it on the
// consumer group lag, from zero replicas when the queue is idle.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/config"
	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/queue"
	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/worker"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	err := run(ctx, os.Getenv, log, nil)
	stop()
	if err != nil {
		log.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}

func load(getenv config.Getenv) (string, string, queueSettings, worker.Config, error) {
	host, _ := os.Hostname()
	listen := config.String(getenv, "METRICS_ADDR", ":9090")
	redisAddr := config.String(getenv, "REDIS_ADDR", "redis:6379")
	qs := queueSettings{
		stream: config.String(getenv, "STREAM", "jobs"),
		group:  config.String(getenv, "CONSUMER_GROUP", "workers"),
	}
	wc := worker.Config{Consumer: config.String(getenv, "POD_NAME", host)}
	var err error
	ints := []struct {
		key string
		dst *int
		def int
	}{
		{"CONCURRENCY", &wc.Concurrency, 4},
		{"WORK_ITERATIONS", &wc.WorkIterations, 200},
	}
	for _, i := range ints {
		if *i.dst, err = config.Int(getenv, i.key, i.def); err != nil {
			return "", "", qs, wc, err
		}
	}
	durations := []struct {
		key string
		dst *time.Duration
		def time.Duration
	}{
		{"PROCESS_TIME", &wc.ProcessTime, 100 * time.Millisecond},
		{"READ_BLOCK", &wc.Block, 2 * time.Second},
		{"RECLAIM_IDLE", &wc.ReclaimIdle, time.Minute},
		{"RECLAIM_EVERY", &wc.ReclaimEvery, 15 * time.Second},
	}
	for _, d := range durations {
		if *d.dst, err = config.Duration(getenv, d.key, d.def); err != nil {
			return "", "", qs, wc, err
		}
	}
	return listen, redisAddr, qs, wc, nil
}

type queueSettings struct{ stream, group string }

// run processes jobs until ctx is cancelled and returns once every job in
// flight has been acknowledged. onListen, if set, receives the metrics address.
func run(ctx context.Context, getenv config.Getenv, log *slog.Logger, onListen func(string)) error {
	listen, redisAddr, qs, wc, err := load(getenv)
	if err != nil {
		return err
	}
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer func() { _ = client.Close() }()
	q := queue.New(client, qs.stream, qs.group, 0)

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	w := worker.New(wc, q, log, reg)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok\n"))
	})
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "err", err)
		}
	}()
	if onListen != nil {
		onListen(ln.Addr().String())
	}

	// Reading needs the consumer group; wait for Redis instead of crash-looping.
	for {
		if err := q.EnsureGroup(ctx); err == nil {
			break
		} else if ctx.Err() == nil {
			log.Warn("consumer group not ready, retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return shutdown(ctx, srv)
		case <-time.After(2 * time.Second):
		}
	}

	log.Info("worker started", "consumer", wc.Consumer, "concurrency", wc.Concurrency,
		"process_time", wc.ProcessTime.String(), "version", version)
	w.Run(ctx)
	return shutdown(ctx, srv)
}

func shutdown(ctx context.Context, srv *http.Server) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("metrics shutdown: %w", err)
	}
	return nil
}
