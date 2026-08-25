// Package callback reports sandbox results back to the producer over
// HTTP, authenticated with the per-task callback secret as a Bearer
// token.
//
// Delivery: Client.Report/ReportFailure retry a few times locally with
// exponential backoff. If every local attempt fails, the report is
// queued on a separate, durable Redis Stream instead of surfacing an
// error — this deliberately does NOT fail the asynq task, since that
// would force the whole task (including its already-completed sandbox
// run) to be retried just to resend a report. A separate consumer on
// the producer side is expected to retry delivery from that stream.
package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Jiffy-Agnet/worker/internal/config"
	"github.com/Jiffy-Agnet/worker/internal/sandbox"

	"github.com/redis/go-redis/v9"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// payload is the JSON body sent to the producer, covering both the
// success and failure cases.
type payload struct {
	Success bool   `json:"success"`
	Report  string `json:"report,omitempty"`
	PRURL   string `json:"pr_url,omitempty"`
	Error   string `json:"error,omitempty"`
}

// streamAdder is the one Redis operation Client needs — kept as a
// narrow interface so tests can substitute a fake instead of a real
// Redis connection.
type streamAdder interface {
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
}

// Client delivers callbacks, with local retries and a durable fallback.
type Client struct {
	rdb                  streamAdder
	maxAttempts          int
	backoff              time.Duration
	failedCallbackStream string
}

// NewClient builds a Client from Worker config, including its own Redis
// connection for the durable failed-callback fallback.
func NewClient(cfg *config.Config) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:      cfg.RedisAddr,
		Password:  cfg.RedisPassword,
		TLSConfig: cfg.RedisTLSConfig(),
	})
	return &Client{
		rdb:                  rdb,
		maxAttempts:          cfg.CallbackMaxAttempts,
		backoff:              cfg.CallbackRetryBackoff,
		failedCallbackStream: cfg.FailedCallbackStream,
	}
}

// Report sends a successful result to callbackURL, authenticated with
// secret as a Bearer token in the Authorization header.
func (c *Client) Report(ctx context.Context, callbackURL, secret string, result sandbox.Result) error {
	return c.deliver(ctx, callbackURL, secret, payload{
		Success: result.Success,
		Report:  result.Report,
		PRURL:   result.PRURL,
	})
}

// ReportFailure sends failure details to callbackURL, authenticated the
// same way.
func (c *Client) ReportFailure(ctx context.Context, callbackURL, secret string, cause error) error {
	return c.deliver(ctx, callbackURL, secret, payload{
		Success: false,
		Error:   cause.Error(),
	})
}

// deliver tries the callback up to maxAttempts times with exponential
// backoff. If every attempt fails, the report is queued on
// failedCallbackStream for the producer to retry independently, and
// this returns nil: a delivery failure here should not, by itself,
// force the whole task (and its already-completed sandbox run) to be
// retried. It only returns an error if that durable hand-off itself
// fails too — the true worst case, where the failure would otherwise
// vanish silently.
func (c *Client) deliver(ctx context.Context, callbackURL, secret string, p payload) error {
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if lastErr = post(ctx, callbackURL, secret, p); lastErr == nil {
			return nil
		}
		if attempt < c.maxAttempts {
			if err := sleepOrDone(ctx, c.backoff*time.Duration(1<<(attempt-1))); err != nil {
				lastErr = err
				break
			}
		}
	}

	// Use a fresh context for the hand-off: it should still happen even
	// if the original ctx is what caused the last attempt to abort.
	if err := c.enqueueFailedCallback(context.Background(), callbackURL, secret, p); err != nil {
		return fmt.Errorf("callback: delivery failed (%v) and could not queue for retry: %w", lastErr, err)
	}
	return nil
}

// failedCallback is what gets queued on failedCallbackStream: enough
// for the producer to retry delivery on its own without needing
// anything back from Worker.
type failedCallback struct {
	CallbackURL    string    `json:"callback_url"`
	CallbackSecret string    `json:"callback_secret"`
	Payload        payload   `json:"payload"`
	FailedAt       time.Time `json:"failed_at"`
}

func (c *Client) enqueueFailedCallback(ctx context.Context, callbackURL, secret string, p payload) error {
	body, err := json.Marshal(failedCallback{
		CallbackURL:    callbackURL,
		CallbackSecret: secret,
		Payload:        p,
		FailedAt:       time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("encode failed-callback entry: %w", err)
	}

	return c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: c.failedCallbackStream,
		Values: map[string]interface{}{"payload": string(body)},
	}).Err()
}

func sleepOrDone(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func post(ctx context.Context, callbackURL, secret string, p payload) error {
	if callbackURL == "" {
		return fmt.Errorf("callback: URL is required")
	}

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("callback: encode payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("callback: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("callback: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("callback: producer returned %s: %s", resp.Status, respBody)
	}
	return nil
}
