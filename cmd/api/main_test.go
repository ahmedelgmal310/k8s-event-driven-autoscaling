package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/queue"
)

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRunServesAndShutsDownGracefully(t *testing.T) {
	mr := miniredis.RunT(t)
	env := getenv(map[string]string{
		"LISTEN_ADDR":          "127.0.0.1:0",
		"REDIS_ADDR":           mr.Addr(),
		"WORK_ITERATIONS":      "5",
		"QUEUE_WATCH_INTERVAL": "10ms",
		"SHUTDOWN_DELAY":       "10ms",
	})
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan string, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- run(ctx, env, slog.New(slog.NewTextHandler(io.Discard, nil)), func(a string) { addr <- a })
	}()

	var base string
	select {
	case a := <-addr:
		base = "http://" + a
	case err := <-errc:
		t.Fatalf("run returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/readyz"},
		{http.MethodGet, "/api/v1/work"},
		{http.MethodPost, "/api/v1/jobs"},
	} {
		req, err := http.NewRequestWithContext(context.Background(), tc.method, base+tc.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s = %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// The consumer group is created in the background.
	deadline := time.Now().Add(10 * time.Second)
	for !mr.Exists("jobs") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !mr.Exists("jobs") {
		t.Fatal("stream was not created")
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, key := range []string{"STREAM_MAXLEN", "WORK_ITERATIONS", "SHUTDOWN_DELAY", "SHUTDOWN_TIMEOUT", "QUEUE_WATCH_INTERVAL"} {
		err := run(context.Background(), getenv(map[string]string{key: "bogus"}), log, nil)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s: err = %v", key, err)
		}
	}
	err := run(context.Background(), getenv(map[string]string{"LISTEN_ADDR": "256.0.0.1:bad"}), log, nil)
	if err == nil {
		t.Fatal("expected listen error")
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(getenv(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.listen != ":8080" || cfg.stream != "jobs" || cfg.group != "workers" || cfg.work != 1000 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestEnsureGroupStopsWithContext(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close() // Redis is down: ensureGroup keeps retrying until cancelled.
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		ensureGroup(ctx, queue.New(client, "jobs", "workers", 0), slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ensureGroup did not stop when its context was cancelled")
	}
}
