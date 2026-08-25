package callback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jiffy-Agnet/worker/internal/sandbox"

	"github.com/redis/go-redis/v9"
)

// fakeStreamAdder stands in for Redis in tests, so the durable-hand-off
// path can be exercised without a real Redis connection.
type fakeStreamAdder struct {
	added []redis.XAddArgs
	err   error
}

func (f *fakeStreamAdder) XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd {
	f.added = append(f.added, *a)
	cmd := redis.NewStringCmd(ctx)
	if f.err != nil {
		cmd.SetErr(f.err)
	} else {
		cmd.SetVal("0-1")
	}
	return cmd
}

func newTestClient(fake *fakeStreamAdder) *Client {
	return &Client{
		rdb:                  fake,
		maxAttempts:          2,
		backoff:              time.Millisecond, // keep tests fast
		failedCallbackStream: "jiffy:failed-callbacks",
	}
}

func TestReportSendsBearerTokenAndPayload(t *testing.T) {
	var gotAuth string
	var gotBody payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(&fakeStreamAdder{})
	err := c.Report(context.Background(), srv.URL, "sekret", sandbox.Result{
		Success: true,
		Report:  "did the thing",
		PRURL:   "https://github.com/owner/repo/pull/1",
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	if want := "Bearer sekret"; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
	if !gotBody.Success || gotBody.Report != "did the thing" || gotBody.PRURL != "https://github.com/owner/repo/pull/1" {
		t.Errorf("unexpected payload: %+v", gotBody)
	}
}

func TestReportFailureSendsErrorMessage(t *testing.T) {
	var gotBody payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(&fakeStreamAdder{})
	err := c.ReportFailure(context.Background(), srv.URL, "sekret", errors.New("boom"))
	if err != nil {
		t.Fatalf("ReportFailure: %v", err)
	}
	if gotBody.Success {
		t.Error("expected Success=false for a failure report")
	}
	if gotBody.Error != "boom" {
		t.Errorf("Error = %q, want %q", gotBody.Error, "boom")
	}
}

func TestDeliverRetriesLocallyThenSucceeds(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fake := &fakeStreamAdder{}
	c := newTestClient(fake)
	if err := c.Report(context.Background(), srv.URL, "sekret", sandbox.Result{Success: true}); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if len(fake.added) != 0 {
		t.Error("should not have queued a failed callback after a successful retry")
	}
}

func TestDeliverQueuesFailedCallbackAfterExhaustingRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	fake := &fakeStreamAdder{}
	c := newTestClient(fake)
	err := c.Report(context.Background(), srv.URL, "sekret", sandbox.Result{Success: true, Report: "x"})
	if err != nil {
		t.Fatalf("Report should not return an error once queued for retry, got: %v", err)
	}
	if len(fake.added) != 1 {
		t.Fatalf("expected 1 queued failed-callback entry, got %d", len(fake.added))
	}

	values, ok := fake.added[0].Values.(map[string]interface{})
	if !ok {
		t.Fatal("queued entry has unexpected Values type")
	}
	raw, ok := values["payload"].(string)
	if !ok {
		t.Fatal("queued entry missing string payload field")
	}

	var entry failedCallback
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("unmarshal queued entry: %v", err)
	}
	if entry.CallbackURL != srv.URL {
		t.Errorf("queued CallbackURL = %q, want %q", entry.CallbackURL, srv.URL)
	}
	if entry.CallbackSecret != "sekret" {
		t.Errorf("queued CallbackSecret = %q, want %q", entry.CallbackSecret, "sekret")
	}
}

func TestDeliverReturnsErrorWhenQueueingAlsoFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(&fakeStreamAdder{err: errors.New("redis down")})
	err := c.Report(context.Background(), srv.URL, "sekret", sandbox.Result{Success: true})
	if err == nil {
		t.Fatal("expected an error when both delivery and the durable hand-off fail")
	}
}

func TestPostRequiresURL(t *testing.T) {
	if err := post(context.Background(), "", "sekret", payload{}); err == nil {
		t.Error("expected an error when callback URL is empty")
	}
}
