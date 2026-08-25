// Package stream bridges task descriptors from a Redis Stream into the local
// asynq queue.
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Jiffy-Agnet/worker/internal/config"
	"github.com/Jiffy-Agnet/worker/internal/task"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// Consumer reads task descriptors from one Redis Stream consumer group and
// submits them to the worker's local asynq queue.
type Consumer struct {
	cfg       *config.Config
	redis     *redis.Client
	asynq     *asynq.Client
	readBlock time.Duration
}

// NewConsumer creates a Redis Stream consumer using the worker configuration.
func NewConsumer(cfg *config.Config) *Consumer {
	redisClient := redis.NewClient(&redis.Options{
		Addr:      cfg.RedisAddr,
		Password:  cfg.RedisPassword,
		TLSConfig: cfg.RedisTLSConfig(),
	})

	return &Consumer{
		cfg:   cfg,
		redis: redisClient,
		asynq: asynq.NewClient(asynq.RedisClientOpt{
			Addr:      cfg.RedisAddr,
			Password:  cfg.RedisPassword,
			TLSConfig: cfg.RedisTLSConfig(),
		}),
		readBlock: 5 * time.Second,
	}
}

// Run consumes messages until ctx is cancelled. A message is acknowledged
// only after it has been accepted by asynq, preserving at-least-once delivery
// when either Redis or the local queue is temporarily unavailable.
func (c *Consumer) Run(ctx context.Context) error {
	if c == nil || c.cfg == nil {
		return fmt.Errorf("stream: consumer configuration is required")
	}
	defer c.redis.Close()
	defer c.asynq.Close()

	if err := c.ensureGroup(ctx); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		messages, err := c.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.cfg.ConsumerGroup,
			Consumer: c.cfg.ConsumerName,
			Streams:  []string{c.cfg.StreamName, ">"},
			Count:    1,
			Block:    c.readBlock,
			NoAck:    false,
		}).Result()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if err == redis.Nil {
				continue
			}
			return fmt.Errorf("stream: read group: %w", err)
		}

		for _, result := range messages {
			for _, message := range result.Messages {
				if err := c.dispatch(ctx, message); err != nil {
					return err
				}
			}
		}
	}
}

func (c *Consumer) ensureGroup(ctx context.Context) error {
	err := c.redis.XGroupCreateMkStream(ctx, c.cfg.StreamName, c.cfg.ConsumerGroup, "$").Err()
	if err == nil || strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return fmt.Errorf("stream: create consumer group: %w", err)
}

func (c *Consumer) dispatch(ctx context.Context, message redis.XMessage) error {
	payload, err := messagePayload(message.Values)
	if err != nil {
		return fmt.Errorf("stream: message %s: %w", message.ID, err)
	}

	if !json.Valid([]byte(payload)) {
		return fmt.Errorf("stream: message %s: payload is not valid JSON", message.ID)
	}

	if _, err := c.asynq.EnqueueContext(ctx, asynq.NewTask(task.TypeExecute, []byte(payload))); err != nil {
		return fmt.Errorf("stream: enqueue message %s: %w", message.ID, err)
	}
	if err := c.redis.XAck(ctx, c.cfg.StreamName, c.cfg.ConsumerGroup, message.ID).Err(); err != nil {
		return fmt.Errorf("stream: acknowledge message %s: %w", message.ID, err)
	}
	return nil
}

func messagePayload(values map[string]interface{}) (string, error) {
	for _, key := range []string{"payload", "data", "descriptor", "body"} {
		if value, ok := values[key]; ok {
			payload := fmt.Sprint(value)
			if payload == "" {
				return "", fmt.Errorf("empty %s field", key)
			}
			return payload, nil
		}
	}

	if len(values) == 0 {
		return "", fmt.Errorf("message has no fields")
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal message fields: %w", err)
	}
	return string(payload), nil
}
