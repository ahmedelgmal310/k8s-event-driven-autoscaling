// Command api serves the HTTP API: CPU-bound requests (the API autoscaler's
// signal) and job submission into the Redis Stream (the worker autoscaler's signal).
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

	"github.com/redis/go-redis/v9"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/api"
	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/config"
	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/queue"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	err := run(ctx, os.Getenv, log, nil)
	stop()
	if err != nil {
		log.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

type settings struct {
	listen, redisAddr, stream, group, pod string
	maxLen                                int
	work                                  int
	shutdownDelay, shutdownTimeout, watch time.Duration
}

func load(getenv config.Getenv) (settings, error) {
	host, _ := os.Hostname()
	s := settings{
		listen:    config.String(getenv, "LISTEN_ADDR", ":8080"),
		redisAddr: config.String(getenv, "REDIS_ADDR", "redis:6379"),
		stream:    config.String(getenv, "STREAM", "jobs"),
		group:     config.String(getenv, "CONSUMER_GROUP", "workers"),
		pod:       config.String(getenv, "POD_NAME", host),
	}
	var err error
	if s.maxLen, err = config.Int(getenv, "STREAM_MAXLEN", 100000); err != nil {
		return s, err
	}
	if s.work, err = config.Int(getenv, "WORK_ITERATIONS", 1000); err != nil {
		return s, err
	}
	if s.shutdownDelay, err = config.Duration(getenv, "SHUTDOWN_DELAY", 0); err != nil {
		return s, err
	}
	if s.shutdownTimeout, err = config.Duration(getenv, "SHUTDOWN_TIMEOUT", 20*time.Second); err != nil {
		return s, err
	}
	if s.watch, err = config.Duration(getenv, "QUEUE_WATCH_INTERVAL", 5*time.Second); err != nil {
		return s, err
	}
	return s, nil
}

// run serves until ctx is cancelled, then shuts down gracefully: readiness
// fails first, in-flight requests finish, and only then does run return.
// onListen, if set, receives the bound address.
func run(ctx context.Context, getenv config.Getenv, log *slog.Logger, onListen func(string)) error {
	cfg, err := load(getenv)
	if err != nil {
		return err
	}
	client := redis.NewClient(&redis.Options{Addr: cfg.redisAddr})
	defer func() { _ = client.Close() }()
	q := queue.New(client, cfg.stream, cfg.group, int64(cfg.maxLen))

	srv := api.New(api.Config{Pod: cfg.pod, WorkIterations: cfg.work}, q, log)

	// Background loops outlive ctx until run returns (they stop via cancelBg).
	bg, cancelBg := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelBg()
	// The consumer group is created by whichever side starts first; the API
	// does not depend on Redis for readiness, so a Redis restart never takes
	// the request path down.
	go ensureGroup(bg, q, log)
	go srv.WatchQueue(bg, cfg.watch)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.listen, err)
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	srv.SetReady(true)
	log.Info("api listening", "addr", ln.Addr().String(), "version", version, "work_iterations", cfg.work)
	if onListen != nil {
		onListen(ln.Addr().String())
	}

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// Kubernetes removes the pod from the endpoints at the same time it sends
	// SIGTERM (after the preStop hook). Failing readiness and optionally
	// waiting keeps accepting the stragglers routed before the update.
	srv.SetReady(false)
	log.Info("shutting down", "delay", cfg.shutdownDelay.String())
	time.Sleep(cfg.shutdownDelay)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("api stopped cleanly")
	return nil
}

func ensureGroup(ctx context.Context, q *queue.Redis, log *slog.Logger) {
	for {
		err := q.EnsureGroup(ctx)
		if err == nil {
			return
		}
		log.Warn("consumer group not ready, retrying", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
