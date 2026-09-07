// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
)

// bannedJobBody is a queued workflow_job for entityID carrying a label that
// would otherwise match a real pool.
func bannedJobBody(entityID int64) []byte {
	return mustJSON(map[string]any{
		"action":       "queued",
		"installation": map[string]any{"id": float64(1)},
		"repository": map[string]any{
			"id": float64(2), "full_name": "banned/repo",
			"owner": map[string]any{"id": float64(entityID), "type": "Organization", "login": "banned-org"},
		},
		"workflow_job": map[string]any{
			"id":       float64(7),
			"name":     "build",
			"labels":   []any{"ubuntu-24.04-riscv"},
			"html_url": "https://example.com",
			"steps":    []any{"a"},
		},
	})
}

func withBan(t *testing.T, id int64) {
	t.Helper()
	saved := BannedEntities
	BannedEntities = append(slices.Clone(BannedEntities), id)
	t.Cleanup(func() { BannedEntities = saved })
}

// TestBannedEntity_JobDropped asserts a banned entity's queued job never
// reaches storage, leaves an audit row, and answers 200 so GitHub stops
// redelivering.
func TestBannedEntity_JobDropped(t *testing.T) {
	const entityID = 999000111
	app, db := newTestApp()
	withBan(t, entityID)

	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, bannedJobBody(entityID), "workflow_job", "2167633"))

	if w.Code != 200 {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if len(db.Jobs) != 0 {
		t.Fatalf("banned entity job stored: %+v", db.Jobs)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeBannedEntity) {
		t.Fatalf("expected one banned_entity row, got %+v", db.Events)
	}
	row := db.Events[0].Row
	if row.EntityID == nil || *row.EntityID != entityID {
		t.Errorf("entity_id=%v want %d", row.EntityID, entityID)
	}
	var payload map[string]any
	if err := json.Unmarshal(db.Events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	job, _ := payload["workflow_job"].(map[string]any)
	if _, has := job["steps"]; has {
		t.Errorf("steps leaked into banned_entity row")
	}
	if job["html_url"] != "https://example.com" {
		t.Errorf("html_url lost: %v", job["html_url"])
	}
}

// TestBannedEntity_OtherActionsDropped covers the ban sitting ahead of the
// action filter: nothing from the entity is acted on, whatever the action.
func TestBannedEntity_OtherActionsDropped(t *testing.T) {
	const entityID = 999000222
	app, db := newTestApp()
	withBan(t, entityID)

	for _, action := range []string{"in_progress", "completed", "waiting"} {
		body := mustJSON(map[string]any{
			"action":       action,
			"installation": map[string]any{"id": float64(1)},
			"repository": map[string]any{
				"id": float64(2), "full_name": "banned/repo",
				"owner": map[string]any{"id": float64(entityID), "type": "Organization", "login": "banned-org"},
			},
			"workflow_job": map[string]any{"id": float64(7), "labels": []any{"ubuntu-24.04-riscv"}},
		})
		w := httptest.NewRecorder()
		app.handleWebhook(w, signedRequest(t, body, "workflow_job", "2167633"))
		if w.Code != 200 {
			t.Errorf("action=%s status=%d want 200", action, w.Code)
		}
	}
	if len(db.Events) != 3 {
		t.Fatalf("expected 3 audit rows, got %d", len(db.Events))
	}
	for i, e := range db.Events {
		if e.Row.Outcome != string(internal.OutcomeBannedEntity) {
			t.Errorf("row %d outcome=%s want banned_entity", i, e.Row.Outcome)
		}
	}
}

// TestBannedEntity_UnbannedUnaffected guards the ban check against swallowing
// everyone else.
func TestBannedEntity_UnbannedUnaffected(t *testing.T) {
	app, db := newTestApp()
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, bannedJobBody(424242), "workflow_job", "2167633"))

	if len(db.Jobs) != 1 {
		t.Fatalf("job not stored for unbanned entity: %+v", db.Jobs)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobStored) {
		t.Fatalf("expected job_stored, got %+v", db.Events)
	}
}

// TestBannedEntities_WellFormed catches a slip in the hard-coded list: a
// non-positive id bans nobody, a duplicate is a stray copy-paste.
func TestBannedEntities_WellFormed(t *testing.T) {
	seen := make(map[int64]bool, len(BannedEntities))
	for _, id := range BannedEntities {
		if id <= 0 {
			t.Errorf("invalid ban entry %d", id)
		}
		if seen[id] {
			t.Errorf("duplicate ban entry %d", id)
		}
		seen[id] = true
	}
}
