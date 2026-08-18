// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
)

func TestI64s(t *testing.T) {
	if got := i64s(42); got != "42" {
		t.Errorf("got %q", got)
	}
}

func TestOrgRunnerKey_EntityAndTarget(t *testing.T) {
	org := orgRunnerKey{EntityType: internal.EntityOrganization, EntityName: "acme", EntityID: 9, InstallationID: 1}
	if got := org.Target(); got != "acme" {
		t.Errorf("org target=%q", got)
	}
	if e := org.Entity(); e.Name != "acme" || e.ID != 9 || e.Type != internal.EntityOrganization {
		t.Errorf("org entity=%+v", e)
	}
	if got := org.String(); got == "" {
		t.Errorf("empty stringify")
	}

	user := orgRunnerKey{EntityType: internal.EntityUser, EntityName: "luhenry", EntityID: 7, RepoFullName: "luhenry/repo"}
	if got := user.Target(); got != "luhenry/repo" {
		t.Errorf("user target=%q", got)
	}
}

func TestRunnerKeyForWorker(t *testing.T) {
	repo := "luhenry/repo"
	w := internal.Worker{
		EntityType:     "User",
		EntityID:       7,
		EntityName:     "luhenry",
		InstallationID: 1,
		RepoFullName:   &repo,
	}
	k := runnerKeyForWorker(w)
	if k.EntityType != internal.EntityUser || k.RepoFullName != "luhenry/repo" {
		t.Errorf("got %+v", k)
	}

	// Org: repo not set
	w2 := internal.Worker{EntityType: "Organization", EntityID: 9, EntityName: "acme", InstallationID: 1}
	k2 := runnerKeyForWorker(w2)
	if k2.RepoFullName != "" {
		t.Errorf("org should not carry repo: %+v", k2)
	}
}

func TestGhAuthenticate_Success(t *testing.T) {
	app, _, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(instID, appID int64) (string, error) {
		if appID != internal.GHAppOrgID {
			t.Errorf("expected org app id, got %d", appID)
		}
		return "tok", nil
	}
	tok, err := app.ghAuthenticate(context.Background(), 1,
		internal.Entity{Type: internal.EntityOrganization, Name: "acme", ID: 9}, authCtx{})
	if err != nil || tok != "tok" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
}

func TestGhAuthenticate_404RecordsRow(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) {
		return "", &internal.GitHubAPIError{StatusCode: 404, Message: "not found"}
	}
	_, err := app.ghAuthenticate(context.Background(), 5,
		internal.Entity{Type: internal.EntityOrganization, Name: "acme", ID: 9},
		authCtx{RepoFullName: "acme/r", JobID: 7})
	if err == nil {
		t.Fatal("expected error returned")
	}
	if len(db.Events) != 1 {
		t.Fatalf("expected one event row, got %d", len(db.Events))
	}
	row := db.Events[0].Row
	if row.Source != "scheduler" || row.Event != "auth_attempt.404" || row.Outcome != string(internal.OutcomeAuth404) {
		t.Errorf("row=%+v", row)
	}
	// Payload contains repo + job
	var p authPayload
	if err := json.Unmarshal(db.Events[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Repository == nil || p.Repository.FullName != "acme/r" {
		t.Errorf("payload repo=%+v", p.Repository)
	}
	if p.WorkflowJob == nil || p.WorkflowJob.ID != 7 {
		t.Errorf("payload job=%+v", p.WorkflowJob)
	}
}

func TestGhAuthenticate_OtherAPIError(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) {
		return "", &internal.GitHubAPIError{StatusCode: 500, Message: "boom"}
	}
	_, err := app.ghAuthenticate(context.Background(), 5,
		internal.Entity{Type: internal.EntityUser, Name: "luhenry", ID: 7}, authCtx{})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeAuthOtherError) {
		t.Errorf("row=%+v", db.Events)
	}
}

func TestGhAuthenticate_NonAPIErrorBubblesUp(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "", errBoom }
	_, err := app.ghAuthenticate(context.Background(), 5,
		internal.Entity{Type: internal.EntityOrganization, Name: "acme", ID: 9}, authCtx{})
	if err == nil {
		t.Fatal("expected error")
	}
	// No installation_events row is written for non-API errors
	if len(db.Events) != 0 {
		t.Errorf("unexpected rows: %+v", db.Events)
	}
}

func TestGhAuthenticate_AddEventErrorSwallowed(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) {
		return "", &internal.GitHubAPIError{StatusCode: 404, Message: "n"}
	}
	db.OnAddEvent = func(internal.InstallationEvent, []byte) (int64, error) { return 0, errBoom }
	_, err := app.ghAuthenticate(context.Background(), 1,
		internal.Entity{Type: internal.EntityOrganization, Name: "acme", ID: 9}, authCtx{})
	if err == nil {
		t.Fatal("expected the 404 error to bubble out regardless of DB failure")
	}
}
