// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
)

// Ids no ban list may contain, so a case that expects service reaching the
// entity keeps working when a real ban is added.
const (
	cleanEntityID = 424242
	cleanSenderID = 4242
)

// bannedJobBody is a queued workflow_job carrying a label that would otherwise
// match a real pool. visibility is "public" so these cases exercise the ban
// lists rather than tripping the non-public-repository gate ahead of them.
func bannedJobBody(entityID, senderID int64) []byte {
	return mustJSON(map[string]any{
		"action":       "queued",
		"installation": map[string]any{"id": float64(1)},
		"sender":       map[string]any{"id": float64(senderID)},
		"repository": map[string]any{
			"id": float64(2), "full_name": "banned/repo", "visibility": "public",
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

// withBan bans id for the duration of the test, so these cases neither depend
// on the shipped list nor outlive an entry that gets lifted.
func withBan(t *testing.T, list *[]int64, id int64) {
	t.Helper()
	saved := *list
	*list = append(slices.Clone(saved), id)
	t.Cleanup(func() { *list = saved })
}

// TestBannedEntity_JobDropped asserts a banned entity's queued job never
// reaches storage, leaves an audit row, and answers 200 so GitHub stops
// redelivering.
func TestBannedEntity_JobDropped(t *testing.T) {
	const entityID = 999000111
	app, db := newTestApp()
	withBan(t, &BannedEntities, entityID)

	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, bannedJobBody(entityID, cleanSenderID), "workflow_job", "2167633"))

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

// TestBannedSender_JobDropped covers the sender list on its own: the org is
// welcome, the person triggering the run is not.
func TestBannedSender_JobDropped(t *testing.T) {
	const senderID = 999000333
	app, db := newTestApp()
	withBan(t, &BannedSenders, senderID)

	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, bannedJobBody(cleanEntityID, senderID), "workflow_job", "2167633"))

	if w.Code != 200 {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if len(db.Jobs) != 0 {
		t.Fatalf("banned sender job stored: %+v", db.Jobs)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeBannedEntity) {
		t.Fatalf("expected one banned_entity row, got %+v", db.Events)
	}
	// The row is keyed by the entity, not the sender: that is the only id the
	// trace endpoints can look up.
	row := db.Events[0].Row
	if row.EntityID == nil || *row.EntityID != cleanEntityID {
		t.Errorf("entity_id=%v want %d", row.EntityID, cleanEntityID)
	}
}

// TestBannedSender_OtherSendersUnaffected pins the ban to the sender id rather
// than the whole org.
func TestBannedSender_OtherSendersUnaffected(t *testing.T) {
	app, db := newTestApp()
	withBan(t, &BannedSenders, 999000444)

	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, bannedJobBody(cleanEntityID, cleanSenderID), "workflow_job", "2167633"))

	if len(db.Jobs) != 1 {
		t.Fatalf("job not stored for unbanned sender: %+v", db.Jobs)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobStored) {
		t.Fatalf("expected job_stored, got %+v", db.Events)
	}
}

// TestBanned_OtherActionsDropped covers the ban sitting ahead of the action
// filter: nothing from the entity is acted on, whatever the action.
func TestBanned_OtherActionsDropped(t *testing.T) {
	const entityID = 999000222
	app, db := newTestApp()
	withBan(t, &BannedEntities, entityID)

	for _, action := range []string{"in_progress", "completed", "waiting"} {
		body := mustJSON(map[string]any{
			"action":       action,
			"installation": map[string]any{"id": float64(1)},
			"sender":       map[string]any{"id": float64(cleanSenderID)},
			"repository": map[string]any{
				"id": float64(2), "full_name": "banned/repo", "visibility": "public",
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

// TestBanned_BeatsStagingProxy pins the check order: a banned entity must not
// reach the staging environment either.
func TestBanned_BeatsStagingProxy(t *testing.T) {
	rt := &stubRT{err: errors.New("staging must not be called")}
	app, db := prodAppWithProxy(rt)
	withBan(t, &BannedEntities, internal.RiseprojectStagingOrgID)

	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, stagingPayload(), "workflow_job", "2167633"))

	if rt.gotReq != nil {
		t.Error("banned entity was forwarded to staging")
	}
	if w.Code != 200 {
		t.Errorf("status=%d want 200", w.Code)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeBannedEntity) {
		t.Fatalf("expected one banned_entity row, got %+v", db.Events)
	}
}

// TestBanned_UnbannedUnaffected guards the ban check against swallowing
// everyone else.
func TestBanned_UnbannedUnaffected(t *testing.T) {
	app, db := newTestApp()
	w := httptest.NewRecorder()
	app.handleWebhook(w, signedRequest(t, bannedJobBody(cleanEntityID, cleanSenderID), "workflow_job", "2167633"))

	if len(db.Jobs) != 1 {
		t.Fatalf("job not stored for unbanned entity: %+v", db.Jobs)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeJobStored) {
		t.Fatalf("expected job_stored, got %+v", db.Events)
	}
}

// TestBanLists_WellFormed catches a slip in the hard-coded lists: a
// non-positive id bans nobody, a duplicate is a stray copy-paste, and an id
// the fixtures treat as clean would break the tests above.
func TestBanLists_WellFormed(t *testing.T) {
	for name, list := range map[string][]int64{"BannedEntities": BannedEntities, "BannedSenders": BannedSenders} {
		seen := make(map[int64]bool, len(list))
		for _, id := range list {
			switch {
			case id <= 0:
				t.Errorf("%s: invalid id %d", name, id)
			case seen[id]:
				t.Errorf("%s: duplicate id %d", name, id)
			case id == cleanEntityID || id == cleanSenderID:
				t.Errorf("%s: id %d is reserved by the test fixtures", name, id)
			}
			seen[id] = true
		}
	}
}
