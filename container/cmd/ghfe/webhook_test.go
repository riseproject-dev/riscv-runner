// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riseproject-dev/riscv-runner/container/internal"
	"github.com/riseproject-dev/riscv-runner/container/internal/testutil"
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
// installation_target, ignored_event. Each must produce exactly one row, and
// each must record the right WebhookOutcome verbatim into installation_events.outcome.
func TestWebhook_InstallationEvents(t *testing.T) {
	cases := []struct {
		event       string
		payload     map[string]any
		wantEvent   string
		wantOutcome internal.WebhookOutcome
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
			wantEvent:   "installation.created",
			wantOutcome: internal.OutcomeOK,
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
			wantEvent:   "installation_repositories.added",
			wantOutcome: internal.OutcomeOK,
		},
		{
			event: "installation_target",
			payload: map[string]any{
				"action":       "renamed",
				"target_type":  "Organization",
				"account":      map[string]any{"id": float64(42), "login": "new"},
				"installation": map[string]any{"id": float64(1)},
			},
			wantEvent:   "installation_target.renamed",
			wantOutcome: internal.OutcomeOK,
		},
		{
			event:       "unknown_event",
			payload:     map[string]any{},
			wantEvent:   "unknown_event",
			wantOutcome: internal.OutcomeIgnoredEvent,
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
			row := db.Events[0].Row
			if row.Event != tc.wantEvent {
				t.Errorf("event=%q want %q", row.Event, tc.wantEvent)
			}
			if row.Outcome != string(tc.wantOutcome) {
				t.Errorf("outcome=%q want %q", row.Outcome, tc.wantOutcome)
			}
			if row.Source != "webhook" {
				t.Errorf("source=%q want webhook", row.Source)
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
			"labels":   []any{"ubuntu-98.04-riscv"},
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
	// CONTRACT §2 entity-extraction table: workflow_job uses repository.owner.{id,login}
	// and repository.owner.type, and installation.id is captured.
	row := db.Events[0].Row
	if row.EntityID == nil || *row.EntityID != 99 {
		t.Errorf("entity_id=%v want 99", row.EntityID)
	}
	if row.EntityName == nil || *row.EntityName != "x" {
		t.Errorf("entity_name=%v want x", row.EntityName)
	}
	if row.EntityType == nil || *row.EntityType != "Organization" {
		t.Errorf("entity_type=%v want Organization", row.EntityType)
	}
	if row.InstallationID == nil || *row.InstallationID != 1 {
		t.Errorf("installation_id=%v want 1", row.InstallationID)
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

// TestWebhook_BadHeaderAndJSONPaths covers the early-return error branches:
// missing body, bad signature, invalid app-id, malformed JSON.
func TestWebhook_BadHeaderAndJSONPaths(t *testing.T) {
	app, _ := newTestApp()

	// Missing event header
	r := httptest.NewRequest("POST", "/", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	app.handleWebhook(w, r)
	if w.Code != 400 {
		t.Errorf("missing event: status=%d", w.Code)
	}

	// Missing app-id header
	body := []byte(`{"zen":"hi"}`)
	r = signedRequest(t, body, "ping", "")
	r.Header.Del(internal.HookAppIDHeader)
	w = httptest.NewRecorder()
	app.handleWebhook(w, r)
	if w.Code != 400 {
		t.Errorf("missing app id: status=%d", w.Code)
	}

	// Non-numeric app id
	r = signedRequest(t, body, "ping", "abc")
	w = httptest.NewRecorder()
	app.handleWebhook(w, r)
	if w.Code != 400 {
		t.Errorf("bad app id: status=%d", w.Code)
	}

	// Invalid JSON body (signature still valid since we sign the literal body)
	bad := []byte(`{bogus`)
	r = signedRequest(t, bad, "ping", "1")
	w = httptest.NewRecorder()
	app.handleWebhook(w, r)
	if w.Code != 400 {
		t.Errorf("invalid json: status=%d", w.Code)
	}
}

// TestWebhook_WorkflowJob_MissingPayloadParts covers the missing-required-field
// branches in handleWorkflowJobEvent.
func TestWebhook_WorkflowJob_MissingPayloadParts(t *testing.T) {
	app, _ := newTestApp()

	cases := []struct {
		name    string
		payload map[string]any
		want    int
	}{
		{
			"missing workflow_job",
			map[string]any{
				"action":       "queued",
				"installation": map[string]any{"id": float64(1)},
				"repository":   map[string]any{"id": float64(2), "full_name": "x/y", "owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"}},
			},
			400,
		},
		{
			"missing owner",
			map[string]any{
				"action":       "queued",
				"installation": map[string]any{"id": float64(1)},
				"repository":   map[string]any{"id": float64(2), "full_name": "x/y"},
				"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}},
			},
			400,
		},
		{
			"missing job id",
			map[string]any{
				"action":       "queued",
				"installation": map[string]any{"id": float64(1)},
				"repository":   map[string]any{"id": float64(2), "full_name": "x/y", "owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"}},
				"workflow_job": map[string]any{"labels": []any{"ubuntu-24.04-riscv"}},
			},
			400,
		},
		{
			"missing repo full_name",
			map[string]any{
				"action":       "queued",
				"installation": map[string]any{"id": float64(1)},
				"repository":   map[string]any{"id": float64(2), "owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"}},
				"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}},
			},
			400,
		},
		{
			"missing repo id",
			map[string]any{
				"action":       "queued",
				"installation": map[string]any{"id": float64(1)},
				"repository":   map[string]any{"full_name": "x/y", "owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"}},
				"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}},
			},
			400,
		},
		{
			"missing owner id",
			map[string]any{
				"action":       "queued",
				"installation": map[string]any{"id": float64(1)},
				"repository":   map[string]any{"id": float64(2), "full_name": "x/y", "owner": map[string]any{"type": "Organization", "login": "x"}},
				"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}},
			},
			400,
		},
		{
			"unsupported entity type",
			map[string]any{
				"action":       "queued",
				"installation": map[string]any{"id": float64(1)},
				"repository":   map[string]any{"id": float64(2), "full_name": "x/y", "owner": map[string]any{"id": float64(99), "type": "Bot", "login": "x"}},
				"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}},
			},
			400,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			app.handleWebhook(w, signedRequest(t, mustJSON(tc.payload), "workflow_job", "2167633"))
			if w.Code != tc.want {
				t.Errorf("status=%d want %d body=%q", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestWebhook_WorkflowJob_QueuedMissingInstallOrURL covers the
// inner branches after label matching (install id / html url / entity name).
func TestWebhook_WorkflowJob_QueuedMissingInstallOrURL(t *testing.T) {
	app, _ := newTestApp()
	base := func(over map[string]any) []byte {
		p := map[string]any{
			"action":       "queued",
			"installation": map[string]any{"id": float64(1)},
			"repository": map[string]any{
				"id": float64(2), "full_name": "x/y",
				"owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"},
			},
			"workflow_job": map[string]any{
				"id":       float64(7),
				"labels":   []any{"ubuntu-24.04-riscv"},
				"html_url": "https://example.com",
			},
		}
		for k, v := range over {
			p[k] = v
		}
		return mustJSON(p)
	}

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{
			"missing installation id",
			base(map[string]any{"installation": map[string]any{}}),
		},
		{
			"missing html_url",
			base(map[string]any{"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}}}),
		},
		{
			"missing entity login",
			base(map[string]any{"repository": map[string]any{"id": float64(2), "full_name": "x/y", "owner": map[string]any{"id": float64(99), "type": "Organization"}}}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			app.handleWebhook(w, signedRequest(t, tc.body, "workflow_job", "2167633"))
			if w.Code != 400 {
				t.Errorf("status=%d want 400 body=%q", w.Code, w.Body.String())
			}
		})
	}
}

// TestWebhook_InProgressAndCompleted exercises the in_progress / completed
// branches incl. job-not-found fallbacks. Each success / not-found case must
// record the corresponding WebhookOutcome into installation_events.
func TestWebhook_InProgressAndCompleted(t *testing.T) {
	body := func(action string) []byte {
		return mustJSON(map[string]any{
			"action":       action,
			"installation": map[string]any{"id": float64(1)},
			"repository": map[string]any{
				"id": float64(2), "full_name": "x/y",
				"owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"},
			},
			"workflow_job": map[string]any{
				"id":          float64(7),
				"labels":      []any{"ubuntu-24.04-riscv"},
				"html_url":    "https://example.com",
				"runner_name": "r-1",
			},
		})
	}

	// in_progress + DB has pending → outcome=job_marked_running, body says "marked running".
	{
		app, db := newTestApp()
		db.OnMarkJobRunning = func(internal.GHJob) (string, error) { return "pending", nil }
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, body("in_progress"), "workflow_job", "2167633"))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "marked running") {
			t.Errorf("in_progress ok: status=%d body=%q", w.Code, w.Body.String())
		}
		if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobMarkedRunning) {
			t.Errorf("expected outcome=job_marked_running, got %+v", db.Events)
		}
	}

	// in_progress + DB has no row → outcome=job_not_found.
	{
		app, db := newTestApp()
		db.OnMarkJobRunning = func(internal.GHJob) (string, error) { return "", nil }
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, body("in_progress"), "workflow_job", "2167633"))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "not found") {
			t.Errorf("status=%d body=%q", w.Code, w.Body.String())
		}
		if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobNotFound) {
			t.Errorf("expected outcome=job_not_found, got %+v", db.Events)
		}
	}

	// in_progress + DB error → 500, no installation_events row (DB write failed
	// before recordEvent ran). Internal 5xx errors are not anchored to an outcome.
	{
		app, db := newTestApp()
		db.OnMarkJobRunning = func(internal.GHJob) (string, error) { return "", errBoom }
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, body("in_progress"), "workflow_job", "2167633"))
		if w.Code != 500 {
			t.Errorf("in_progress err: status=%d", w.Code)
		}
		if len(db.Events) != 0 {
			t.Errorf("DB-error path should not record an installation_events row, got %+v", db.Events)
		}
	}

	// completed + DB had running → outcome=job_marked_completed.
	{
		app, db := newTestApp()
		db.OnMarkJobComplete = func(internal.GHJob) (string, error) { return "running", nil }
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, body("completed"), "workflow_job", "2167633"))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "completed") {
			t.Errorf("completed: status=%d body=%q", w.Code, w.Body.String())
		}
		if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobMarkedCompleted) {
			t.Errorf("expected outcome=job_marked_completed, got %+v", db.Events)
		}
	}

	// completed + DB has no row → outcome=job_not_found.
	{
		app, db := newTestApp()
		db.OnMarkJobComplete = func(internal.GHJob) (string, error) { return "", nil }
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, body("completed"), "workflow_job", "2167633"))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "not found") {
			t.Errorf("status=%d body=%q", w.Code, w.Body.String())
		}
		if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobNotFound) {
			t.Errorf("expected outcome=job_not_found, got %+v", db.Events)
		}
	}

	// completed + DB error → 500, no row.
	{
		app, db := newTestApp()
		db.OnMarkJobComplete = func(internal.GHJob) (string, error) { return "", errBoom }
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, body("completed"), "workflow_job", "2167633"))
		if w.Code != 500 {
			t.Errorf("completed err: status=%d", w.Code)
		}
		if len(db.Events) != 0 {
			t.Errorf("DB-error path should not record an installation_events row, got %+v", db.Events)
		}
	}
}

// TestWebhook_QueuedAddJobError covers the AddJob DB-error branch.
func TestWebhook_QueuedAddJobError(t *testing.T) {
	app, db := newTestApp()
	db.OnAddJob = func(internal.GHJob, internal.Entity, string, string, int64, string, string, string, []string) (bool, error) {
		return false, errBoom
	}
	body := mustJSON(map[string]any{
		"action":       "queued",
		"installation": map[string]any{"id": float64(1)},
		"repository": map[string]any{
			"id": float64(2), "full_name": "x/y",
			"owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"},
		},
		"workflow_job": map[string]any{
			"id":       float64(7),
			"labels":   []any{"ubuntu-24.04-riscv"},
			"html_url": "https://example.com",
		},
	})
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, body, "workflow_job", "2167633"))
	if w.Code != 500 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

// TestWebhook_QueuedAlreadyExists covers the stored=false branch.
func TestWebhook_QueuedAlreadyExists(t *testing.T) {
	app, db := newTestApp()
	db.OnAddJob = func(internal.GHJob, internal.Entity, string, string, int64, string, string, string, []string) (bool, error) {
		return false, nil
	}
	body := mustJSON(map[string]any{
		"action":       "queued",
		"installation": map[string]any{"id": float64(1)},
		"repository": map[string]any{
			"id": float64(2), "full_name": "x/y",
			"owner": map[string]any{"id": float64(99), "type": "Organization", "login": "x"},
		},
		"workflow_job": map[string]any{
			"id":       float64(7),
			"labels":   []any{"ubuntu-24.04-riscv"},
			"html_url": "https://example.com",
		},
	})
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, body, "workflow_job", "2167633"))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("body=%q", w.Body.String())
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobAlreadyExists) {
		t.Errorf("outcome=%+v", db.Events)
	}
}

// TestWebhook_InstallationEvent_Missing covers the bad-payload branches in
// handleInstallationEvent / handleInstallationTargetEvent.
func TestWebhook_InstallationEvent_Missing(t *testing.T) {
	app, _ := newTestApp()

	cases := []struct {
		event   string
		payload map[string]any
	}{
		{"installation", map[string]any{"action": "created"}},                                                                   // no installation
		{"installation", map[string]any{"action": "created", "installation": map[string]any{"id": float64(1)}}},                 // no account
		{"installation_target", map[string]any{"action": "renamed"}},                                                            // no account
		{"installation_target", map[string]any{"action": "renamed", "account": map[string]any{"login": "n", "id": float64(1)}}}, // no installation
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, mustJSON(tc.payload), tc.event, "2167633"))
		if w.Code != 400 {
			t.Errorf("%s: status=%d body=%q", tc.event, w.Code, w.Body.String())
		}
	}
}

// TestAsInt64 covers each numeric input path.
func TestAsInt64(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{float64(7), 7},
		{int64(8), 8},
		{int(9), 9},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := asInt64(c.in); got != c.want {
			t.Errorf("asInt64(%v)=%d want %d", c.in, got, c.want)
		}
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
