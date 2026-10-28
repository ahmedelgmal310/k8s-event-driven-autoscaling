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

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRunProcessesJobsAndStops(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	q := queue.New(client, "jobs", "workers", 0)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := q.Enqueue(ctx, "{}"); err != nil {
			t.Fatal(err)
		}
	}

	env := getenv(map[string]string{
		"METRICS_ADDR": "127.0.0.1:0",
		"REDIS_ADDR":   mr.Addr(),
		"PROCESS_TIME": "1ms",
		"READ_BLOCK":   "20ms",
		"POD_NAME":     "worker-test",
	})
	runCtx, cancel := context.WithCancel(ctx)
	addr := make(chan string, 1)
	errc := make(chan error, 1)
	go func() { errc <- run(runCtx, env, discard, func(a string) { addr <- a }) }()
	base := "http://" + <-addr

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := q.Stats(ctx)
		if err == nil && st.Processed == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs not processed: %+v, %v", st, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `worker_jobs_processed_total{result="ok"} 5`) {
		t.Fatalf("metrics missing processed count:\n%s", body)
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if resp, err = http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v", err)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestRunWaitsForRedisAndStopsOnCancel(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	env := getenv(map[string]string{"METRICS_ADDR": "127.0.0.1:0", "REDIS_ADDR": addr})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := run(ctx, env, discard, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	for _, key := range []string{"CONCURRENCY", "WORK_ITERATIONS", "PROCESS_TIME", "READ_BLOCK", "RECLAIM_IDLE", "RECLAIM_EVERY"} {
		err := run(context.Background(), getenv(map[string]string{key: "bogus"}), discard, nil)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s: err = %v", key, err)
		}
	}
	if err := run(context.Background(), getenv(map[string]string{"METRICS_ADDR": "256.0.0.1:bad"}), discard, nil); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestLoadDefaults(t *testing.T) {
	listen, redisAddr, qs, wc, err := load(getenv(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if listen != ":9090" || redisAddr != "redis:6379" || qs.stream != "jobs" || wc.Concurrency != 4 || wc.ProcessTime != 100*time.Millisecond {
		t.Fatalf("defaults: %s %s %+v %+v", listen, redisAddr, qs, wc)
	}
}
