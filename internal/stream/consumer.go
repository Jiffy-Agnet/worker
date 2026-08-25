// Package stream implements the Gateway<->Worker boundary: a plain Redis
// Stream, independent of both Celery's and asynq's own wire formats — see
// docs/adr/ADR-distributed-stateless-workers.md, "Dispatch protocol".
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/Jiffy-Agnet/worker/internal/config"
	"github.com/Jiffy-Agnet/worker/internal/task"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

var errUnexpectedPayload = errors.New(`stream message missing string "payload" field`)

const (
	// claimMinIdle is how long a message must sit unacknowledged in a
	// consumer's Pending Entries List before we treat that consumer as
	// dead and reclaim the message. Because we ack right after a
	// successful local enqueue (not after the sandbox finishes), normal
	// idle time is milliseconds — this only fires on a genuine crash
	// between XReadGroup and XAck.
	claimMinIdle = 5 * time.Minute

	// claimSweepInterval is how often we check for reclaimable messages.
	claimSweepInterval = 30 * time.Second

	// enqueueUniqueTTL deduplicates re-enqueues of the same message (e.g.
	// a reclaim firing for a message whose original consumer had already
	// enqueued it, but crashed before acking). Must comfortably exceed
	// claimMinIdle so a reclaim-triggered re-enqueue is always still
	// covered by the original enqueue's uniqueness window.
	enqueueUniqueTTL = 30 * time.Minute
)

// Consumer reads task descriptors off the shared Redis Stream and hands
// each one to the local asynq queue for execution.
type Consumer struct {
	cfg    *config.Config
	rdb    *redis.Client
	client *asynq.Client
}

func NewConsumer(cfg *config.Config) *Consumer {
	rdb := redis.NewClient(&redis.Options{
		Addr:      cfg.RedisAddr,
		Password:  cfg.RedisPassword,
		TLSConfig: cfg.RedisTLSConfig(),
	})
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:      cfg.RedisAddr,
		Password:  cfg.RedisPassword,
		TLSConfig: cfg.RedisTLSConfig(),
	})
	return &Consumer{cfg: cfg, rdb: rdb, client: client}
}

// Run blocks, reading from the stream and bridging each message into the
// local asynq queue. A Worker only claims a new message when it has a free
// local execution slot — see the ADR, "Capacity model: sum of Worker
// concurrency, no central scheduler".
//
// A background sweep (see reclaimLoop) recovers messages left pending by a
// consumer that crashed between reading a message and acknowledging it —
// see the ADR section on reliability / at-least-once delivery.
func (c *Consumer) Run(ctx context.Context) error {
	// Consumer group creation is idempotent; ignore "already exists" errors.
	_ = c.rdb.XGroupCreateMkStream(ctx, c.cfg.StreamName, c.cfg.ConsumerGroup, "$").Err()

	go c.reclaimLoop(ctx)

	for {
		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.cfg.ConsumerGroup,
			Consumer: c.cfg.ConsumerName,
			Streams:  []string{c.cfg.StreamName, ">"},
			Count:    1,
			Block:    0,
		}).Result()
		if err != nil {
			log.Printf("stream read: %v", err)
			continue
		}

		for _, s := range streams {
			for _, msg := range s.Messages {
				if err := c.dispatch(ctx, msg); err != nil {
					log.Printf("dispatch %s: %v", msg.ID, err)
					continue
				}
				c.rdb.XAck(ctx, c.cfg.StreamName, c.cfg.ConsumerGroup, msg.ID)
			}
		}
	}
}

// reclaimLoop periodically re-claims messages that have been pending
// (read but never acknowledged) for longer than claimMinIdle. This
// recovers tasks whose original consumer crashed before it could enqueue
// the task locally and ack the stream entry — without this, such a
// message would stay stuck in the Pending Entries List forever and its
// task would never run.
//
// This delivers at-least-once, not exactly-once: dispatch() below
// deduplicates the case where the original consumer *did* enqueue the
// task but crashed before acking, so a reclaim never causes the sandbox
// to run twice for the same message.
func (c *Consumer) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(claimSweepInterval)
	defer ticker.Stop()

	cursor := "0-0"
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		messages, next, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   c.cfg.StreamName,
			Group:    c.cfg.ConsumerGroup,
			Consumer: c.cfg.ConsumerName,
			MinIdle:  claimMinIdle,
			Start:    cursor,
			Count:    10,
		}).Result()
		if err != nil {
			log.Printf("reclaim sweep: %v", err)
			continue
		}
		cursor = next

		for _, msg := range messages {
			// TODO(next task): check this message's delivery count via
			// XPENDING before redelivering. After N attempts, stop
			// reclaiming and report failure to Gateway/Edge instead of
			// dispatching again — tracked as a follow-up (retry-cap +
			// failure reporting).
			log.Printf("reclaim: redelivering %s (original consumer likely crashed)", msg.ID)
			if err := c.dispatch(ctx, msg); err != nil {
				log.Printf("reclaim dispatch %s: %v", msg.ID, err)
				continue
			}
			c.rdb.XAck(ctx, c.cfg.StreamName, c.cfg.ConsumerGroup, msg.ID)
		}
	}
}

func (c *Consumer) dispatch(ctx context.Context, msg redis.XMessage) error {
	raw, ok := msg.Values["payload"].(string)
	if !ok {
		return errUnexpectedPayload
	}

	var descriptor task.Descriptor
	if err := json.Unmarshal([]byte(raw), &descriptor); err != nil {
		return err
	}
	// TODO: validate descriptor.SchemaVersion against the versions this
	// Worker build understands (see ADR-XXX, Action Item 1).

	t := asynq.NewTask(task.TypeExecute, []byte(raw))
	_, err := c.client.EnqueueContext(ctx, t, asynq.Unique(enqueueUniqueTTL))
	if err != nil && !errors.Is(err, asynq.ErrDuplicateTask) {
		return err
	}
	// asynq.ErrDuplicateTask means this exact message was already
	// enqueued — most likely by the original consumer, right before it
	// crashed and left this stream entry unacknowledged (see
	// reclaimLoop). The work is already queued exactly once; safe to ack
	// and move on rather than treating this as a failure.
	return nil
}
