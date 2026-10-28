// Package queue is a small job queue on top of a Redis Stream and a consumer
// group. Producers append with XADD, workers read with XREADGROUP and
// acknowledge with XACK, so a job that a worker took but never acknowledged
// stays in the group's pending list and can be reclaimed by another worker.
//
// The consumer group "lag" (entries not yet delivered to any consumer) is the
// signal KEDA scales the workers on, including scaling to and from zero.
package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// PayloadField is the stream entry field that carries the job payload.
const PayloadField = "payload"

// Message is one job read from the stream.
type Message struct {
	ID      string
	Payload string
}

// Stats describes the backlog of the stream for one consumer group.
type Stats struct {
	Stream string `json:"stream"`
	Group  string `json:"group"`
	// Length is the number of entries kept in the stream (XLEN).
	Length int64 `json:"length"`
	// Lag is the number of entries not yet delivered to the group.
	// It is -1 when Redis cannot compute it.
	Lag int64 `json:"lag"`
	// Pending is the number of delivered but unacknowledged entries.
	Pending int64 `json:"pending"`
	// Consumers is the number of consumers registered in the group.
	Consumers int64 `json:"consumers"`
	// Processed counts acknowledged jobs (at-least-once, so redeliveries count twice).
	Processed int64 `json:"processed"`
}

// Backlog is the work that is still outstanding: undelivered plus unacknowledged.
func (s Stats) Backlog() int64 {
	lag := s.Lag
	if lag < 0 {
		lag = 0
	}
	return lag + s.Pending
}

// Redis is a queue backed by a Redis Stream.
type Redis struct {
	client redis.UniversalClient
	stream string
	group  string
	maxLen int64
}

// New returns a queue for the given stream and consumer group. maxLen caps the
// stream length approximately (0 means no cap).
func New(client redis.UniversalClient, stream, group string, maxLen int64) *Redis {
	return &Redis{client: client, stream: stream, group: group, maxLen: maxLen}
}

func (q *Redis) processedKey() string { return q.stream + ":processed" }

// EnsureGroup creates the stream and the consumer group if they do not exist.
// The group starts at ID 0 so that jobs enqueued before any worker existed are
// still counted as lag and delivered.
func (q *Redis) EnsureGroup(ctx context.Context) error {
	err := q.client.XGroupCreateMkStream(ctx, q.stream, q.group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create consumer group %q on %q: %w", q.group, q.stream, err)
	}
	return nil
}

// Ping checks the connection to Redis.
func (q *Redis) Ping(ctx context.Context) error {
	return q.client.Ping(ctx).Err()
}

// Enqueue appends a job and returns its stream ID.
func (q *Redis) Enqueue(ctx context.Context, payload string) (string, error) {
	args := &redis.XAddArgs{
		Stream: q.stream,
		Values: map[string]any{PayloadField: payload},
	}
	if q.maxLen > 0 {
		args.MaxLen = q.maxLen
		args.Approx = true
	}
	id, err := q.client.XAdd(ctx, args).Result()
	if err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return id, nil
}

// Read blocks up to block for at most count new jobs for this consumer.
// It returns an empty slice when no job arrived in time.
func (q *Redis) Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]Message, error) {
	res, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    q.group,
		Consumer: consumer,
		Streams:  []string{q.stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var out []Message
	for _, s := range res {
		out = append(out, toMessages(s.Messages)...)
	}
	return out, nil
}

// Reclaim takes over jobs that another consumer has held for longer than
// minIdle without acknowledging them (for example because its pod crashed).
func (q *Redis) Reclaim(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]Message, error) {
	msgs, _, err := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   q.stream,
		Group:    q.group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    "0-0",
		Count:    count,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("reclaim: %w", err)
	}
	return toMessages(msgs), nil
}

// Ack acknowledges a job and increments the processed counter atomically.
func (q *Redis) Ack(ctx context.Context, id string) error {
	_, err := q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.XAck(ctx, q.stream, q.group, id)
		p.Incr(ctx, q.processedKey())
		return nil
	})
	if err != nil {
		return fmt.Errorf("ack %s: %w", id, err)
	}
	return nil
}

// RemoveConsumer deletes the consumer from the group when it holds no pending
// jobs, so scaled-in pods do not accumulate in XINFO output. A consumer that
// still owns pending jobs is kept, because deleting it would drop them from
// the pending list. It reports whether the consumer was removed.
func (q *Redis) RemoveConsumer(ctx context.Context, consumer string) (bool, error) {
	pending, err := q.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream:   q.stream,
		Group:    q.group,
		Start:    "-",
		End:      "+",
		Count:    1,
		Consumer: consumer,
	}).Result()
	if err != nil {
		return false, fmt.Errorf("pending for %s: %w", consumer, err)
	}
	if len(pending) > 0 {
		return false, nil
	}
	if err := q.client.XGroupDelConsumer(ctx, q.stream, q.group, consumer).Err(); err != nil {
		return false, fmt.Errorf("delete consumer %s: %w", consumer, err)
	}
	return true, nil
}

// Stats returns the current backlog of the consumer group.
func (q *Redis) Stats(ctx context.Context) (Stats, error) {
	st := Stats{Stream: q.stream, Group: q.group, Lag: -1}
	groups, err := q.client.XInfoGroups(ctx, q.stream).Result()
	if err != nil {
		return st, fmt.Errorf("xinfo groups: %w", err)
	}
	found := false
	for _, g := range groups {
		if g.Name == q.group {
			st.Lag = g.Lag
			st.Pending = g.Pending
			st.Consumers = g.Consumers
			found = true
			break
		}
	}
	if !found {
		return st, fmt.Errorf("consumer group %q not found on %q", q.group, q.stream)
	}
	if st.Length, err = q.client.XLen(ctx, q.stream).Result(); err != nil {
		return st, fmt.Errorf("xlen: %w", err)
	}
	processed, err := q.client.Get(ctx, q.processedKey()).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return st, fmt.Errorf("processed counter: %w", err)
	}
	st.Processed = processed
	return st, nil
}

func toMessages(in []redis.XMessage) []Message {
	out := make([]Message, 0, len(in))
	for _, m := range in {
		p, _ := m.Values[PayloadField].(string)
		out = append(out, Message{ID: m.ID, Payload: p})
	}
	return out
}
