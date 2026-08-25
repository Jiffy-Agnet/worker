// Package config loads Worker-local settings from the environment only.
// The Worker never persists anything to disk beyond an optional repo cache
// (see ADR-XXX, "fully stateless").
package config

import (
	"crypto/tls"
	"os"
	"strconv"
	"strings"
	"time"
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

	// SandboxMemoryLimit, SandboxMemorySwapLimit, and SandboxCPULimit
	// are passed to `docker run` as --memory/--memory-swap/--cpus for
	// every sandbox execution. Left unset by default (no limit) — must
	// be set deliberately, sized so WORKER_CONCURRENCY x per-task limit
	// fits the host's actual RAM/CPU, before raising concurrency above 1
	// is safe.
	SandboxMemoryLimit     string
	SandboxMemorySwapLimit string
	SandboxCPULimit        string

	// SandboxCleanup controls whether a sandbox container is removed
	// after it exits (`docker run --rm`). Set JIFFY_SANDBOX_CLEANUP=false
	// to leave it running for debugging. Defaults to true.
	SandboxCleanup bool

	// SandboxContainerTTL is a hard backstop on how long a sandbox
	// container may exist, independent of SandboxCleanup and of the
	// task's own status inside it — every container is force-removed
	// this long after its creation regardless. 0 disables the backstop.
	// Task execution itself has no separate time limit; this is the
	// only thing bounding container lifetime. Configured via
	// SANDBOX_CONTAINER_TTL_HOURS.
	SandboxContainerTTL time.Duration

	// SandboxExtraEnv is a resolved list of "NAME=value" pairs forwarded
	// into every sandbox container as-is — e.g. the LLM provider
	// endpoint/key the code agent needs. Only variable *names* are
	// configured (via SANDBOX_ENV_PASSTHROUGH, comma-separated); each
	// value is read from Worker's own environment at startup, never
	// duplicated into another variable.
	SandboxExtraEnv []string
}

func Load() (*Config, error) {
	concurrency, err := strconv.Atoi(getenv("WORKER_CONCURRENCY", "1"))
	if err != nil {
		concurrency = 1
	}

	var ttl time.Duration
	if hours, err := strconv.ParseFloat(os.Getenv("SANDBOX_CONTAINER_TTL_HOURS"), 64); err == nil && hours > 0 {
		ttl = time.Duration(hours * float64(time.Hour))
	}

	var extraEnv []string
	for _, name := range splitAndTrim(os.Getenv("SANDBOX_ENV_PASSTHROUGH")) {
		if v, ok := os.LookupEnv(name); ok {
			extraEnv = append(extraEnv, name+"="+v)
		}
	}

	return &Config{
		RedisAddr:              getenv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:          os.Getenv("REDIS_PASSWORD"),
		RedisTLS:               getenv("REDIS_TLS", "true") == "true",
		StreamName:             getenv("JIFFY_TASK_STREAM", "jiffy:tasks"),
		ConsumerGroup:          getenv("JIFFY_CONSUMER_GROUP", "jiffy-workers"),
		ConsumerName:           getenv("HOSTNAME", "worker-unknown"),
		Concurrency:            concurrency,
		SandboxImage:           os.Getenv("JIFFY_SANDBOX_IMAGE"),
		CallbackSecret:         os.Getenv("JIFFY_CALLBACK_SECRET"),
		RepoCacheDir:           os.Getenv("JIFFY_REPO_CACHE_DIR"),
		SandboxMemoryLimit:     os.Getenv("JIFFY_SANDBOX_MEMORY_LIMIT"),
		SandboxMemorySwapLimit: os.Getenv("SANDBOX_MEMORY_SWAP_LIMIT"),
		SandboxCPULimit:        os.Getenv("JIFFY_SANDBOX_CPU_LIMIT"),
		SandboxCleanup:         getenv("JIFFY_SANDBOX_CLEANUP", "true") == "true",
		SandboxContainerTTL:    ttl,
		SandboxExtraEnv:        extraEnv,
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

// splitAndTrim splits a comma-separated list, trimming whitespace and
// dropping empty entries. Returns nil for an empty input.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
