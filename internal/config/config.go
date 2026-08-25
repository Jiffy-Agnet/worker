// Package config loads Worker-local settings from the environment only.
// The Worker never persists anything to disk beyond an optional repo cache
// (see ADR-XXX, "fully stateless").
package config

import (
	"crypto/tls"
	"net"
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
	// into every sandbox container as-is. Two are built in and entirely
	// optional — OPEN_API_BASE_URL and OPEN_API_KEY, the LLM provider
	// settings the code agent needs, forwarded automatically when set
	// and simply omitted if not, no error either way. Anything else is
	// configured via SANDBOX_ENV_PASSTHROUGH (comma-separated names);
	// each value is read from Worker's own environment at startup,
	// never duplicated into another variable.
	SandboxExtraEnv []string

	// CallbackMaxAttempts and CallbackRetryBackoff control local retries
	// of a single callback delivery (exponential backoff starting at
	// CallbackRetryBackoff). If every local attempt fails, the report
	// is queued on FailedCallbackStream instead of failing the task —
	// see internal/callback.
	CallbackMaxAttempts  int
	CallbackRetryBackoff time.Duration

	// FailedCallbackStream is the Redis Stream a callback delivery is
	// queued on once local retries are exhausted, so the producer can
	// retry delivery independently without Worker re-running the whole
	// task (and its already-completed sandbox execution) just to resend
	// a report.
	FailedCallbackStream string
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
	seen := make(map[string]bool)
	addEnv := func(name, value string) {
		if value == "" || seen[name] {
			return
		}
		seen[name] = true
		extraEnv = append(extraEnv, name+"="+value)
	}

	// The LLM provider settings the code agent needs are optional and
	// may be left unset entirely — forwarded automatically when present,
	// no separate SANDBOX_ENV_PASSTHROUGH entry required for these two.
	addEnv("OPEN_API_BASE_URL", os.Getenv("OPEN_API_BASE_URL"))
	addEnv("OPEN_API_KEY", os.Getenv("OPEN_API_KEY"))

	for _, name := range splitAndTrim(os.Getenv("SANDBOX_ENV_PASSTHROUGH")) {
		if v, ok := os.LookupEnv(name); ok {
			addEnv(name, v)
		}
	}

	callbackMaxAttempts, err := strconv.Atoi(getenv("CALLBACK_MAX_ATTEMPTS", "3"))
	if err != nil || callbackMaxAttempts < 1 {
		callbackMaxAttempts = 3
	}
	callbackBackoffSeconds, err := strconv.Atoi(getenv("CALLBACK_RETRY_BACKOFF_SECONDS", "2"))
	if err != nil || callbackBackoffSeconds < 1 {
		callbackBackoffSeconds = 2
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
		CallbackMaxAttempts:    callbackMaxAttempts,
		CallbackRetryBackoff:   time.Duration(callbackBackoffSeconds) * time.Second,
		FailedCallbackStream:   getenv("FAILED_CALLBACK_STREAM", "jiffy:failed-callbacks"),
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

// SecurityWarnings flags configuration that looks unsafe for a
// non-local deployment — connecting to a remote Redis without TLS
// and/or without a password. Task descriptors carry short-lived git
// credentials and callback secrets, so an unencrypted or unauthenticated
// connection to a Redis instance reachable from outside the host is a
// real exposure, not just a style nit. This never blocks startup; the
// caller decides what to do with the warnings (typically just logging).
func (c *Config) SecurityWarnings() []string {
	var warnings []string
	if isLocalAddr(c.RedisAddr) {
		return warnings
	}
	if !c.RedisTLS {
		warnings = append(warnings, "REDIS_TLS is disabled while REDIS_ADDR does not look like a local address — task descriptors (including credentials) will cross the network in the clear")
	}
	if c.RedisPassword == "" {
		warnings = append(warnings, "REDIS_PASSWORD is not set while REDIS_ADDR does not look like a local address — anyone who can reach this Redis can read or write tasks and callbacks")
	}
	return warnings
}

func isLocalAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
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
