package queue

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestQueue(t *testing.T) (*Redis, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	return New(client, "jobs", "workers", 1000), mr
}

func TestEnsureGroupIsIdempotent(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	for range 2 {
		if err := q.EnsureGroup(ctx); err != nil {
			t.Fatalf("EnsureGroup: %v", err)
		}
	}
	if err := q.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestEnqueueReadAckStats(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a", "b", "c"} {
		if _, err := q.Enqueue(ctx, p); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	st, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Length != 3 || st.Lag != 3 || st.Pending != 0 || st.Backlog() != 3 {
		t.Fatalf("stats before read = %+v", st)
	}

	msgs, err := q.Read(ctx, "w1", 2, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Payload != "a" || msgs[1].Payload != "b" {
		t.Fatalf("Read = %+v", msgs)
	}

	st, _ = q.Stats(ctx)
	// miniredis does not decrement lag on delivery like Redis 7+ does, so only
	// the pending list is asserted here; e2e covers lag against real Redis.
	if st.Pending != 2 || st.Consumers != 1 {
		t.Fatalf("stats after read = %+v", st)
	}

	for _, m := range msgs {
		if err := q.Ack(ctx, m.ID); err != nil {
			t.Fatalf("Ack: %v", err)
		}
	}
	st, _ = q.Stats(ctx)
	if st.Pending != 0 || st.Processed != 2 {
		t.Fatalf("stats after ack = %+v", st)
	}
}

func TestReadEmptyReturnsNoMessages(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	msgs, err := q.Read(ctx, "w1", 1, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected no messages, got %+v", msgs)
	}
}

func TestReclaimAndRemoveConsumer(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(ctx, "orphan"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Read(ctx, "dead", 1, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// The consumer that owns a pending job must not be removed.
	removed, err := q.RemoveConsumer(ctx, "dead")
	if err != nil || removed {
		t.Fatalf("RemoveConsumer(dead) = %v, %v; want false, nil", removed, err)
	}

	msgs, err := q.Reclaim(ctx, "alive", 0, 10)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Payload != "orphan" {
		t.Fatalf("Reclaim = %+v", msgs)
	}
	if err := q.Ack(ctx, msgs[0].ID); err != nil {
		t.Fatal(err)
	}

	removed, err = q.RemoveConsumer(ctx, "dead")
	if err != nil || !removed {
		t.Fatalf("RemoveConsumer(dead) after reclaim = %v, %v; want true, nil", removed, err)
	}
}

func TestStatsErrors(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	if _, err := q.Stats(ctx); err == nil {
		t.Fatal("expected error for missing stream")
	}
	if _, err := q.client.XAdd(ctx, &redis.XAddArgs{Stream: "jobs", Values: map[string]any{"x": "y"}}).Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Stats(ctx); err == nil {
		t.Fatal("expected error for missing group")
	}

	mr.Close()
	if err := q.EnsureGroup(ctx); err == nil {
		t.Fatal("expected error when Redis is down")
	}
	if _, err := q.Enqueue(ctx, "x"); err == nil {
		t.Fatal("expected enqueue error when Redis is down")
	}
	if _, err := q.Read(ctx, "w", 1, time.Millisecond); err == nil {
		t.Fatal("expected read error when Redis is down")
	}
	if _, err := q.Reclaim(ctx, "w", time.Second, 1); err == nil {
		t.Fatal("expected reclaim error when Redis is down")
	}
	if err := q.Ack(ctx, "1-1"); err == nil {
		t.Fatal("expected ack error when Redis is down")
	}
	if _, err := q.RemoveConsumer(ctx, "w"); err == nil {
		t.Fatal("expected remove error when Redis is down")
	}
}

func TestBacklogIgnoresUnknownLag(t *testing.T) {
	if got := (Stats{Lag: -1, Pending: 4}).Backlog(); got != 4 {
		t.Fatalf("Backlog = %d, want 4", got)
	}
}
