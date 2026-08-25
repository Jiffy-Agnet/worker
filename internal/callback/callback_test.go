package callback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Jiffy-Agnet/worker/internal/sandbox"
)

func TestReportSendsBearerTokenAndPayload(t *testing.T) {
	var gotAuth string
	var gotBody payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := Report(context.Background(), srv.URL, "sekret", sandbox.Result{
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

	err := ReportFailure(context.Background(), srv.URL, "sekret", errors.New("boom"))
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

func TestPostFailsOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	err := Report(context.Background(), srv.URL, "sekret", sandbox.Result{Success: true})
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

func TestPostRequiresURL(t *testing.T) {
	if err := Report(context.Background(), "", "sekret", sandbox.Result{}); err == nil {
		t.Error("expected an error when callback URL is empty")
	}
}
