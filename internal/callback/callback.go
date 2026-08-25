// Package callback reports sandbox results back to the producer over
// HTTP, authenticated with the per-task callback secret as a Bearer
// token. This path is unchanged from the existing declarative,
// per-provider callback spec and is already language-agnostic.
package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Jiffy-Agnet/worker/internal/sandbox"
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

// Report sends a successful result to callbackURL, authenticated with
// secret as a Bearer token in the Authorization header.
func Report(ctx context.Context, callbackURL, secret string, result sandbox.Result) error {
	return post(ctx, callbackURL, secret, payload{
		Success: result.Success,
		Report:  result.Report,
		PRURL:   result.PRURL,
	})
}

// ReportFailure sends failure details to callbackURL, authenticated the
// same way.
func ReportFailure(ctx context.Context, callbackURL, secret string, cause error) error {
	return post(ctx, callbackURL, secret, payload{
		Success: false,
		Error:   cause.Error(),
	})
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
