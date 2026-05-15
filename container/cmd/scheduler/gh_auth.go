package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

// authPayload is the JSON body written into installation_events.payload
// when a scheduler auth attempt fails — gives operators enough context to
// diagnose which entity / job triggered it without joining other rows.
type authPayload struct {
	InstallationID int64     `json:"installation_id"`
	AppID          int64     `json:"app_id"`
	EntityType     string    `json:"entity_type"`
	EntityID       int64     `json:"entity_id,omitempty"`
	EntityName     string    `json:"entity_name,omitempty"`
	Repository     *authRepo `json:"repository"`
	WorkflowJob    *authJob  `json:"workflow_job"`
	HTTPStatus     int       `json:"http_status"`
	ErrorMessage   string    `json:"error_message"`
}

type authRepo struct {
	FullName string `json:"full_name"`
}

type authJob struct {
	ID int64 `json:"id"`
}

// authCtx is the optional context passed to ghAuthenticate for richer logs.
type authCtx struct {
	RepoFullName string
	JobID        int64
}

// ghAuthenticate wraps GitHubClient.AuthenticateApp and records each
// failure in installation_events. Successes are not logged: AuthenticateApp
// is cached and called on every iteration, so logging would drown out
// genuine signal.
func (a *App) ghAuthenticate(ctx context.Context, installationID int64, e internal.Entity, ac authCtx) (string, error) {
	var appID int64
	switch e.Type {
	case internal.EntityOrganization:
		appID = internal.GHAppOrgID
	case internal.EntityUser:
		appID = internal.GHAppPersonalID
	default:
		panic("unhandled EntityType: " + string(e.Type))
	}
	token, err := a.GH.AuthenticateApp(ctx, installationID, appID)
	if err == nil {
		return token, nil
	}
	var apiErr *internal.GitHubAPIError
	if !errors.As(err, &apiErr) {
		return "", err
	}

	var outcome internal.WebhookOutcome
	var evt string
	if apiErr.StatusCode == 404 {
		outcome = internal.OutcomeAuth404
		evt = "auth_attempt.404"
	} else {
		outcome = internal.OutcomeAuthOtherError
		evt = "auth_attempt.other_error"
	}

	payload := authPayload{
		InstallationID: installationID,
		AppID:          appID,
		EntityType:     string(e.Type),
		EntityID:       e.ID,
		EntityName:     e.Name,
		HTTPStatus:     apiErr.StatusCode,
		ErrorMessage:   apiErr.Message,
	}
	if ac.RepoFullName != "" {
		payload.Repository = &authRepo{FullName: ac.RepoFullName}
	}
	if ac.JobID != 0 {
		payload.WorkflowJob = &authJob{ID: ac.JobID}
	}
	body, _ := json.Marshal(payload)

	etStr := string(e.Type)
	row := internal.InstallationEvent{
		Source:         "scheduler",
		Event:          evt,
		Outcome:        string(outcome),
		InstallationID: &installationID,
		AppID:          &appID,
		EntityType:     &etStr,
	}
	if e.ID != 0 {
		row.EntityID = &e.ID
	}
	if e.Name != "" {
		row.EntityName = &e.Name
	}
	if _, derr := a.DB.AddInstallationEvent(ctx, row, body); derr != nil {
		slog.Error("Failed to record auth_attempt", "event", evt, "installation_id", installationID, "entity", e, "err", derr)
	}
	return "", err
}

func i64s(v int64) string { return strconv.FormatInt(v, 10) }

// orgRunnerKey groups workers by their GitHub-target scope. EntityName is
// always set; RepoFullName is set only for users (where GitHub registers
// runners under a repo). The struct is comparable so it's usable as a map key.
type orgRunnerKey struct {
	InstallationID int64
	EntityType     internal.EntityType
	EntityID       int64
	EntityName     string
	RepoFullName   string
}

// Entity returns the org / user identity for this scope.
func (k orgRunnerKey) Entity() internal.Entity {
	return internal.Entity{Type: k.EntityType, Name: k.EntityName, ID: k.EntityID}
}

// Target returns whichever of EntityName / RepoFullName identifies the GitHub
// scope where runners are registered: org name for orgs, repo for users.
func (k orgRunnerKey) Target() string {
	switch k.EntityType {
	case internal.EntityOrganization:
		return k.EntityName
	case internal.EntityUser:
		return k.RepoFullName
	default:
		panic("unhandled EntityType: " + string(k.EntityType))
	}
}

func runnerKeyForWorker(w internal.Worker) orgRunnerKey {
	et, _ := internal.ParseEntityType(w.EntityType)
	k := orgRunnerKey{
		InstallationID: w.InstallationID,
		EntityType:     et,
		EntityID:       w.EntityID,
		EntityName:     w.EntityName,
	}
	if et == internal.EntityUser && w.RepoFullName != nil {
		k.RepoFullName = *w.RepoFullName
	}
	return k
}

func (k orgRunnerKey) String() string {
	return fmt.Sprintf("(%d, %s, %d, %s)", k.InstallationID, k.EntityType, k.EntityID, k.Target())
}
