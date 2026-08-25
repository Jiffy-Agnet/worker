package main

import (
	"context"
	"log"

	"github.com/Jiffy-Agnet/worker/internal/config"
	"github.com/Jiffy-Agnet/worker/internal/stream"
	"github.com/Jiffy-Agnet/worker/internal/task"

	"github.com/hibiken/asynq"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// asynq handles local execution concurrency and retries, once a
	// message has been bridged in from the shared Redis Stream — see
	// docs/adr/ADR-distributed-stateless-workers.md, "Dispatch protocol".
	srv := asynq.NewServer(
		asynq.RedisClientOpt{
			Addr:      cfg.RedisAddr,
			Password:  cfg.RedisPassword,
			TLSConfig: cfg.RedisTLSConfig(),
		},
		asynq.Config{Concurrency: cfg.Concurrency},
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc(task.TypeExecute, task.NewExecutor(cfg).HandleAsynqTask)

	go func() {
		if err := srv.Run(mux); err != nil {
			log.Fatalf("asynq server: %v", err)
		}
	}()

	consumer := stream.NewConsumer(cfg)
	if err := consumer.Run(context.Background()); err != nil {
		log.Fatalf("stream consumer: %v", err)
	}
}
