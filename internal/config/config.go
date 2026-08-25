// Package config loads Worker-local settings from the environment only.
// The Worker never persists anything to disk beyond an optional repo cache
// (see ADR-XXX, "fully stateless").
package config

import (
	"crypto/tls"
	"os"
	"strconv"
)

// Config holds everything the Worker needs to run. Nothing here is
// business/queue state — it is safe to lose on restart.
type Config struct {
	RedisAddr      string
	RedisPassword  string
	RedisTLS       bool
	StreamName     string
	ConsumerGroup  string
	ConsumerName   string
	Concurrency    int
	SandboxImage   string
	CallbackSecret string
	RepoCacheDir   string

	// SandboxMemoryLimit and SandboxCPULimit are passed to `docker run`
	// as --memory/--cpus for every sandbox execution. Left unset by
	// default (no limit) — must be set deliberately, sized so
	// WORKER_CONCURRENCY x per-task limit fits the host's actual RAM/CPU,
	// before raising concurrency above 1 is safe.
	SandboxMemoryLimit string
	SandboxCPULimit    string
}

func Load() (*Config, error) {
	concurrency, err := strconv.Atoi(getenv("WORKER_CONCURRENCY", "1"))
	if err != nil {
		concurrency = 1
	}

	return &Config{
		RedisAddr:      getenv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:  os.Getenv("REDIS_PASSWORD"),
		RedisTLS:       getenv("REDIS_TLS", "true") == "true",
		StreamName:     getenv("JIFFY_TASK_STREAM", "jiffy:tasks"),
		ConsumerGroup:  getenv("JIFFY_CONSUMER_GROUP", "jiffy-workers"),
		ConsumerName:   getenv("HOSTNAME", "worker-unknown"),
		Concurrency:    concurrency,
		SandboxImage:   os.Getenv("JIFFY_SANDBOX_IMAGE"),
		CallbackSecret: os.Getenv("JIFFY_CALLBACK_SECRET"),
		RepoCacheDir:       os.Getenv("JIFFY_REPO_CACHE_DIR"),
		SandboxMemoryLimit: os.Getenv("JIFFY_SANDBOX_MEMORY_LIMIT"),
		SandboxCPULimit:    os.Getenv("JIFFY_SANDBOX_CPU_LIMIT"),
	}, nil
}

// RedisTLSConfig returns nil when TLS is disabled, so callers can pass it
// straight into redis/asynq client options.
func (c *Config) RedisTLSConfig() *tls.Config {
	if !c.RedisTLS {
		return nil
	}
	return &tls.Config{}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
