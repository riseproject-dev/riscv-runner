package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/riseproject-dev/riscv-runner-app/container-go/internal"
	"github.com/riseproject-dev/riscv-runner-app/container-go/internal/testutil"
)

const webhookSecret = "test-secret"

func newTestApp() (*App, *testutil.FakeDB) {
	db := testutil.NewFakeDB()
	cfg := internal.Config{
		Prod:          false,
		WebhookSecret: webhookSecret,
		ImageUbuntu24: "img24",
		ImageUbuntu26: "img26",
		RunnerPrefix:  "rise-riscv-runner-staging-",
	}
	return &App{Config: cfg, DB: db, GH: &testutil.FakeGH{}}, db
}

func signedRequest(t *testing.T, body []byte, event, appID string) *http.Request {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	r.Header.Set(internal.HookSignatureHeader, sig)
	r.Header.Set(internal.HookEventHeader, event)
	r.Header.Set(internal.HookAppIDHeader, appID)
	return r
}

// TestWebhook_SignatureMismatch verifies an unsigned request 401s and writes
// no installation_events row (signature is checked before the row gate).
func TestWebhook_SignatureMismatch(t *testing.T) {
	app, db := newTestApp()
	r := httptest.NewRequest("POST", "/", bytes.NewReader([]byte(`{}`)))
	r.Header.Set(internal.HookSignatureHeader, "sha256=bogus")
	r.Header.Set(internal.HookEventHeader, "workflow_job")
	r.Header.Set(internal.HookAppIDHeader, "1")
	w := httptest.NewRecorder()
	app.handleWebhook(w, r)
	if w.Code != 401 {
		t.Fatalf("status=%d want 401", w.Code)
	}
	if len(db.Events) != 0 {
		t.Fatalf("no event row expected, got %d", len(db.Events))
	}
}

// TestWebhook_PingWritesEventRow covers the b909123 invariant for the ping path.
func TestWebhook_PingWritesEventRow(t *testing.T) {
	app, db := newTestApp()
	body := []byte(`{"zen":"hi"}`)
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, body, "ping", "2167633"))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	if len(db.Events) != 1 {
		t.Fatalf("expected 1 event row, got %d", len(db.Events))
	}
	if db.Events[0].Row.Event != "ping" || db.Events[0].Row.Outcome != string(internal.OutcomeOK) {
		t.Errorf("row=%+v", db.Events[0].Row)
	}
}

// TestWebhook_InstallationEvents covers each of: installation, installation_repositories,
// installation_target, ignored_event. Each must produce exactly one row.
func TestWebhook_InstallationEvents(t *testing.T) {
	cases := []struct {
		event   string
		payload map[string]any
		want    string
	}{
		{
			event: "installation",
			payload: map[string]any{
				"action": "created",
				"installation": map[string]any{
					"id":          float64(1),
					"target_id":   float64(99),
					"target_type": "Organization",
					"account":     map[string]any{"login": "org"},
				},
			},
			want: "installation.created",
		},
		{
			event: "installation_repositories",
			payload: map[string]any{
				"action": "added",
				"installation": map[string]any{
					"id":          float64(1),
					"target_id":   float64(99),
					"target_type": "Organization",
					"account":     map[string]any{"login": "org"},
				},
			},
			want: "installation_repositories.added",
		},
		{
			event: "installation_target",
			payload: map[string]any{
				"action":       "renamed",
				"target_type":  "Organization",
				"account":      map[string]any{"id": float64(42), "login": "new"},
				"installation": map[string]any{"id": float64(1)},
			},
			want: "installation_target.renamed",
		},
		{
			event:   "unknown_event",
			payload: map[string]any{},
			want:    "unknown_event",
		},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			app, db := newTestApp()
			body, _ := json.Marshal(tc.payload)
			w := httptest.NewRecorder()
			app.handleWebhook(w, signedRequest(t, body, tc.event, "2167633"))
			if w.Code != 200 {
				t.Fatalf("status=%d", w.Code)
			}
			if len(db.Events) != 1 {
				t.Fatalf("expected 1 event row, got %d", len(db.Events))
			}
			got := db.Events[0].Row.Event
			if got != tc.want {
				t.Errorf("event=%q want %q", got, tc.want)
			}
		})
	}
}

// TestWebhook_WorkflowJob_IgnoredAction asserts unrecognised actions trip the
// ignored_action outcome.
func TestWebhook_WorkflowJob_IgnoredAction(t *testing.T) {
	app, db := newTestApp()
	body := mustJSON(map[string]any{
		"action":       "waiting",
		"installation": map[string]any{"id": float64(1)},
		"repository": map[string]any{
			"id": float64(2), "full_name": "x/y",
			"owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"},
		},
		"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}},
	})
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, body, "workflow_job", "2167633"))
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeIgnoredAction) {
		t.Fatalf("expected ignored_action, got rows=%+v", db.Events)
	}
}

// TestIgnoredNoLabel_PayloadMinimized verifies aae3ab3: ignored_no_label
// keeps only workflow_job.{labels,html_url} + repository.full_name.
func TestIgnoredNoLabel_PayloadMinimized(t *testing.T) {
	app, db := newTestApp()
	body := mustJSON(map[string]any{
		"action":       "queued",
		"installation": map[string]any{"id": float64(1)},
		"repository": map[string]any{
			"id": float64(2), "full_name": "x/y", "url": "drop",
			"owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x", "url": "drop"},
		},
		"workflow_job": map[string]any{
			"id":       float64(7),
			"labels":   []any{"ubuntu-26.04-riscv"},
			"html_url": "https://example.com",
			"url":      "drop",
			"steps":    []any{"a"},
		},
	})
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, body, "workflow_job", "2167633"))
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeIgnoredNoLabel) {
		t.Fatalf("expected ignored_no_label, rows=%+v", db.Events)
	}
	var payload map[string]any
	if err := json.Unmarshal(db.Events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	job, _ := payload["workflow_job"].(map[string]any)
	if job["url"] != nil {
		t.Errorf("workflow_job.url leaked")
	}
	if job["html_url"] != "https://example.com" {
		t.Errorf("html_url lost: %v", job["html_url"])
	}
	if _, has := job["steps"]; has {
		t.Errorf("steps leaked")
	}
	if _, has := payload["sender"]; has {
		t.Errorf("sender leaked")
	}
	repo, _ := payload["repository"].(map[string]any)
	if repo["full_name"] != "x/y" {
		t.Errorf("repository.full_name lost: %v", repo["full_name"])
	}
	if _, has := repo["url"]; has {
		t.Errorf("repository.url leaked")
	}
}

// TestWebhook_QueuedJobStored asserts a valid queued event writes an event
// row with job_stored and persists the job.
func TestWebhook_QueuedJobStored(t *testing.T) {
	app, db := newTestApp()
	body := mustJSON(map[string]any{
		"action":       "queued",
		"installation": map[string]any{"id": float64(1)},
		"repository": map[string]any{
			"id": float64(2), "full_name": "x/y",
			"owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"},
		},
		"workflow_job": map[string]any{
			"id":       float64(7),
			"name":     "build",
			"labels":   []any{"ubuntu-24.04-riscv"},
			"html_url": "https://example.com",
		},
	})
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, body, "workflow_job", "2167633"))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	if len(db.Jobs) != 1 || db.Jobs[0].JobID != 7 {
		t.Fatalf("job not stored: %+v", db.Jobs)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobStored) {
		t.Errorf("event row mismatch: %+v", db.Events)
	}
}

// TestWebhook_BodyTooShortForSignature errors out with 400/401 not a panic.
func TestWebhook_MissingHeaders(t *testing.T) {
	app, _ := newTestApp()
	r := httptest.NewRequest("POST", "/", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	app.handleWebhook(w, r)
	if w.Code != 400 {
		t.Fatalf("expected 400 on missing event header, got %d", w.Code)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
