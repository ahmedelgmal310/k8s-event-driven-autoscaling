package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/ahmedelgmal310/k8s-event-driven-autoscaling/internal/queue"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestWorkerDrainsQueue(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	q := queue.New(client, "jobs", "workers", 0)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	const n = 25
	for range n {
		if _, err := q.Enqueue(ctx, "{}"); err != nil {
			t.Fatal(err)
		}
	}

	w := New(Config{Consumer: "w1", Concurrency: 4, Block: 20 * time.Millisecond, WorkIterations: 10, ProcessTime: time.Millisecond},
		q, discard, prometheus.NewRegistry())
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		w.Run(runCtx)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := q.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.Processed == n && st.Pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue not drained: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if got := testutil.ToFloat64(w.processed.WithLabelValues("ok")); got != n {
		t.Fatalf("processed ok = %v, want %d", got, n)
	}
	st, _ := q.Stats(ctx)
	if st.Consumers != 0 {
		t.Fatalf("consumer should be removed after a clean drain, stats = %+v", st)
	}
}

// fakeQueue hands out a fixed set of messages and records acks.
type fakeQueue struct {
	mu        sync.Mutex
	msgs      []queue.Message
	reclaim   []queue.Message
	readErr   error
	ackErr    error
	removeErr error
	acked     []string
	reads     atomic.Int64
}

func (f *fakeQueue) Read(ctx context.Context, _ string, _ int64, block time.Duration) ([]queue.Message, error) {
	f.reads.Add(1)
	f.mu.Lock()
	if f.readErr != nil {
		err := f.readErr
		f.mu.Unlock()
		return nil, err
	}
	if len(f.msgs) > 0 {
		m := f.msgs[0]
		f.msgs = f.msgs[1:]
		f.mu.Unlock()
		return []queue.Message{m}, nil
	}
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(block):
		return nil, nil
	}
}

func (f *fakeQueue) Reclaim(ctx context.Context, _ string, _ time.Duration, _ int64) ([]queue.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := f.reclaim
	f.reclaim = nil
	return out, nil
}

func (f *fakeQueue) Ack(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acked = append(f.acked, id)
	return nil
}

func (f *fakeQueue) RemoveConsumer(context.Context, string) (bool, error) {
	return f.removeErr == nil, f.removeErr
}

func (f *fakeQueue) ackedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acked)
}

func TestInFlightJobFinishesOnShutdown(t *testing.T) {
	f := &fakeQueue{msgs: []queue.Message{{ID: "1-1"}}}
	w := New(Config{Consumer: "w", ProcessTime: 200 * time.Millisecond, Block: 10 * time.Millisecond}, f, discard, prometheus.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	// Cancel while the job is being processed.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if f.ackedCount() != 1 {
		t.Fatal("job read before shutdown must be acknowledged")
	}
}

func TestReclaimLoop(t *testing.T) {
	f := &fakeQueue{reclaim: []queue.Message{{ID: "9-9"}}}
	w := New(Config{Consumer: "w", ReclaimEvery: 10 * time.Millisecond, Block: 10 * time.Millisecond}, f, discard, prometheus.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for f.ackedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if got := testutil.ToFloat64(w.reclaimed); got != 1 {
		t.Fatalf("reclaimed = %v, want 1", got)
	}
}

func TestErrorsAreCountedAndSurvived(t *testing.T) {
	f := &fakeQueue{readErr: errors.New("down"), removeErr: errors.New("down")}
	w := New(Config{Consumer: "w"}, f, discard, prometheus.NewRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	if got := testutil.ToFloat64(w.errors); got < 1 {
		t.Fatalf("errors = %v, want >= 1", got)
	}

	f2 := &fakeQueue{ackErr: errors.New("ack failed")}
	w2 := New(Config{Consumer: "w"}, f2, discard, prometheus.NewRegistry())
	w2.handle(context.Background(), queue.Message{ID: "1-1"})
	if got := testutil.ToFloat64(w2.processed.WithLabelValues("ack_error")); got != 1 {
		t.Fatalf("ack_error = %v, want 1", got)
	}
}

func TestReclaimErrorIsCounted(t *testing.T) {
	f := &reclaimErrQueue{}
	w := New(Config{Consumer: "w", ReclaimEvery: 5 * time.Millisecond, Block: 5 * time.Millisecond}, f, discard, prometheus.NewRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	if got := testutil.ToFloat64(w.errors); got < 1 {
		t.Fatalf("errors = %v, want >= 1", got)
	}
}

type reclaimErrQueue struct{ fakeQueue }

func (r *reclaimErrQueue) Reclaim(context.Context, string, time.Duration, int64) ([]queue.Message, error) {
	return nil, errors.New("reclaim failed")
}

func TestDefaults(t *testing.T) {
	w := New(Config{}, &fakeQueue{}, discard, prometheus.NewRegistry())
	if w.cfg.Concurrency != 1 || w.cfg.Block != 2*time.Second || w.cfg.AckTimeout != 5*time.Second {
		t.Fatalf("defaults = %+v", w.cfg)
	}
}
