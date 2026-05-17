// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/riseproject-dev/riscv-runner/container/internal"
)

// webhook is POST /, the GitHub-App webhook entry point.
// Every accepted code path writes exactly one installation_events row
// (invariant b909123). Signature / header / JSON failures short-circuit
// before any row is written, since we can't trust the body's identity.
func (a *App) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, 400, "Failed to read body")
		return
	}

	event := r.Header.Get(internal.HookEventHeader)
	if event == "" {
		httpError(w, 400, "Missing X-Github-Event header")
		return
	}
	signature := r.Header.Get(internal.HookSignatureHeader)
	if ok, msg := verifySignature(body, signature, a.Config.WebhookSecret); !ok {
		slog.Warn("Webhook signature verification failed", "reason", msg)
		httpError(w, 401, msg)
		return
	}

	appIDStr := r.Header.Get(internal.HookAppIDHeader)
	if appIDStr == "" {
		httpError(w, 400, "Missing X-GitHub-Hook-Installation-Target-Id header")
		return
	}
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil {
		httpError(w, 400, "Invalid X-GitHub-Hook-Installation-Target-Id header")
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.Debug("Invalid JSON payload")
		httpError(w, 400, "Invalid JSON payload")
		return
	}

	switch event {
	case "ping":
		a.recordEvent(r, eventRecord{Event: "ping", Outcome: internal.OutcomeOK, Payload: payload, AppID: &appID})
		_, _ = w.Write([]byte("pong"))
	case "installation":
		a.handleInstallationEvent(w, r, event, payload, appID)
	case "installation_repositories":
		a.handleInstallationEvent(w, r, event, payload, appID)
	case "installation_target":
		a.handleInstallationTargetEvent(w, r, event, payload, appID)
	case "workflow_job":
		a.handleWorkflowJobEvent(w, r, body, payload, appID)
	default:
		a.recordEvent(r, eventRecord{Event: event, Outcome: internal.OutcomeIgnoredEvent, Payload: payload, AppID: &appID})
		_, _ = w.Write([]byte("Ignoring " + event + " event"))
	}
}

// eventRecord is the per-call template for one installation_events row.
type eventRecord struct {
	Event          string
	Outcome        internal.WebhookOutcome
	Payload        map[string]any
	AppID          *int64
	InstallationID *int64
	EntityType     *string
	EntityID       *int64
	EntityName     *string
}

func (a *App) recordEvent(r *http.Request, e eventRecord) {
	body, _ := json.Marshal(e.Payload)
	row := internal.InstallationEvent{
		Source:         "webhook",
		Event:          e.Event,
		Outcome:        string(e.Outcome),
		InstallationID: e.InstallationID,
		AppID:          e.AppID,
		EntityType:     e.EntityType,
		EntityID:       e.EntityID,
		EntityName:     e.EntityName,
	}
	if _, err := a.DB.AddInstallationEvent(r.Context(), row, body); err != nil {
		slog.Error("Failed to record installation_events row", "event", e.Event, "outcome", e.Outcome, "err", err)
	}
}

func (a *App) handleInstallationEvent(w http.ResponseWriter, r *http.Request, event string, payload map[string]any, appID int64) {
	action, _ := payload["action"].(string)
	install, _ := payload["installation"].(map[string]any)
	if install == nil {
		httpError(w, 400, "Missing installation in payload")
		return
	}
	account, _ := install["account"].(map[string]any)
	if account == nil {
		httpError(w, 400, "Missing installation.account in payload")
		return
	}
	instID := asInt64(install["id"])
	targetID := asInt64(install["target_id"])
	targetType, _ := install["target_type"].(string)
	login, _ := account["login"].(string)

	a.recordEvent(r, eventRecord{
		Event:          event + "." + action,
		Outcome:        internal.OutcomeOK,
		Payload:        payload,
		AppID:          &appID,
		InstallationID: &instID,
		EntityType:     &targetType,
		EntityID:       &targetID,
		EntityName:     &login,
	})
	_, _ = w.Write([]byte(event + "." + action + " logged"))
}

func (a *App) handleInstallationTargetEvent(w http.ResponseWriter, r *http.Request, event string, payload map[string]any, appID int64) {
	action, _ := payload["action"].(string)
	// installation_target.renamed carries the new account at top-level.
	account, _ := payload["account"].(map[string]any)
	install, _ := payload["installation"].(map[string]any)
	if account == nil || install == nil {
		httpError(w, 400, "Missing account or installation in payload")
		return
	}
	targetType, _ := payload["target_type"].(string)
	login, _ := account["login"].(string)
	instID := asInt64(install["id"])
	accID := asInt64(account["id"])

	a.recordEvent(r, eventRecord{
		Event:          event + "." + action,
		Outcome:        internal.OutcomeOK,
		Payload:        payload,
		AppID:          &appID,
		InstallationID: &instID,
		EntityType:     &targetType,
		EntityID:       &accID,
		EntityName:     &login,
	})
	_, _ = w.Write([]byte(event + "." + action + " logged"))
}

func (a *App) handleWorkflowJobEvent(w http.ResponseWriter, r *http.Request, body []byte, payload map[string]any, appID int64) {
	action, _ := payload["action"].(string)
	repo, _ := payload["repository"].(map[string]any)
	install, _ := payload["installation"].(map[string]any)
	job, _ := payload["workflow_job"].(map[string]any)
	if repo == nil || install == nil || job == nil {
		httpError(w, 400, "Missing repository/installation/workflow_job in payload")
		return
	}
	owner, _ := repo["owner"].(map[string]any)
	if owner == nil {
		httpError(w, 400, "Missing repository.owner in payload")
		return
	}
	ownerID := asInt64(owner["id"])
	ownerType, _ := owner["type"].(string)
	ownerLogin, _ := owner["login"].(string)
	installID := asInt64(install["id"])
	repoFullName, _ := repo["full_name"].(string)
	if ownerID == 0 {
		httpError(w, 400, "Owner ID is missing in payload")
		return
	}
	et, err := internal.ParseEntityType(ownerType)
	if err != nil {
		httpError(w, 400, err.Error())
		return
	}
	entity := internal.Entity{Type: et, Name: ownerLogin, ID: ownerID}

	trimmed := trimWorkflowJobPayload(payload)
	base := eventRecord{
		Payload:        trimmed,
		AppID:          &appID,
		InstallationID: &installID,
		EntityType:     (*string)(&entity.Type),
		EntityID:       &entity.ID,
		EntityName:     &entity.Name,
	}

	// Staging proxy: a real repo (e.g. riscv-runner-sample) is wired into
	// the prod app but its webhooks should drive the staging environment.
	// Forward the unmodified body to staging ghfe and short-circuit; the
	// prod instance neither stores nor reconciles the job locally.
	if shouldProxyToStaging(a.Config, entity, repoFullName) {
		base.Event = "workflow_job." + action
		base.Outcome = internal.OutcomeProxiedToStaging
		a.recordEvent(r, base)
		if err := a.proxyToStaging(w, r, body); err != nil {
			slog.Error("Staging proxy failed", "entity", entity, "repo", repoFullName, "err", err)
			httpError(w, 502, "staging proxy failed")
		}
		return
	}

	if action != "queued" && action != "in_progress" && action != "completed" {
		base.Event = "workflow_job." + action
		base.Outcome = internal.OutcomeIgnoredAction
		a.recordEvent(r, base)
		slog.Debug("Ignoring action", "action", action)
		_, _ = w.Write([]byte("Ignoring action: " + action))
		return
	}

	jobID := asInt64(job["id"])
	if jobID == 0 {
		httpError(w, 400, "Job ID is missing in payload")
		return
	}
	labels := jsonStrings(job["labels"])
	if repoFullName == "" {
		httpError(w, 400, "Repository full name is missing in payload")
		return
	}
	if asInt64(repo["id"]) == 0 {
		httpError(w, 400, "Repository ID is missing in payload")
		return
	}

	base.Event = "workflow_job." + action

	pool, image, matched := matchLabelsToK8s(a.Config, entity.ID, repoFullName, labels)
	if !matched {
		// ignored_no_label is the highest-volume row; trim aggressively.
		htmlURL, _ := job["html_url"].(string)
		base.Payload = map[string]any{
			"workflow_job": map[string]any{
				"labels":   labels,
				"html_url": htmlURL,
			},
			"repository": map[string]any{"full_name": repoFullName},
		}
		base.Outcome = internal.OutcomeIgnoredNoLabel
		a.recordEvent(r, base)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("Ignoring job: missing required platform label"))
		return
	}

	jobName, _ := job["name"].(string)
	slog.Info("Received workflow_job",
		"entity", entity,
		"action", action,
		"job_id", jobID,
		"name", jobName,
		"repo", repoFullName,
		"labels", labels,
	)
	enablePerfLog(r)

	switch action {
	case "queued":
		if installID == 0 {
			httpError(w, 400, "Installation ID is missing in payload")
			return
		}
		if ownerLogin == "" {
			httpError(w, 400, "Entity name is missing in payload")
			return
		}
		htmlURL, _ := job["html_url"].(string)
		if htmlURL == "" {
			httpError(w, 400, "HTML URL is missing in payload")
			return
		}
		j := internal.Job{
			JobID:          jobID,
			Provider:       "github",
			EntityID:       entity.ID,
			EntityName:     entity.Name,
			EntityType:     string(entity.Type),
			RepoFullName:   repoFullName,
			InstallationID: installID,
			K8sPool:        pool,
			K8sImage:       image,
			HTMLURL:        &htmlURL,
		}
		stored, err := a.DB.AddJob(r.Context(), j, labels)
		if err != nil {
			slog.Error("AddJob failed", "entity", entity, "job_id", jobID, "err", err)
			httpError(w, 500, "internal error")
			return
		}
		base.Outcome = internal.OutcomeJobStored
		msg := "Job " + i64s(jobID) + " stored."
		if stored {
			slog.Info("Stored job", "entity", entity, "job_id", jobID, "k8s_pool", pool)
		} else {
			base.Outcome = internal.OutcomeJobAlreadyExists
			msg = "Job " + i64s(jobID) + " already exists."
			slog.Debug("Job already exists, skipping", "entity", entity, "job_id", jobID)
		}
		a.recordEvent(r, base)
		_, _ = w.Write([]byte(msg))

	case "in_progress":
		runner, _ := job["runner_name"].(string)
		prev, err := a.DB.MarkJobRunning(r.Context(), jobID, runner)
		if err != nil {
			slog.Error("MarkJobRunning failed", "entity", entity, "job_id", jobID, "err", err)
			httpError(w, 500, "internal error")
			return
		}
		if prev == "" {
			base.Outcome = internal.OutcomeJobNotFound
			a.recordEvent(r, base)
			slog.Warn("Job not found on in_progress event", "entity", entity, "job_id", jobID)
			_, _ = w.Write([]byte("Job " + i64s(jobID) + " not found."))
			return
		}
		base.Outcome = internal.OutcomeJobMarkedRunning
		a.recordEvent(r, base)
		slog.Info("Job marked running", "entity", entity, "job_id", jobID, "prev_status", prev)
		_, _ = w.Write([]byte("Job " + i64s(jobID) + " marked running (was " + prev + ")."))

	case "completed":
		runner, _ := job["runner_name"].(string)
		prev, err := a.DB.MarkJobCompleted(r.Context(), jobID, runner)
		if err != nil {
			slog.Error("MarkJobCompleted failed", "entity", entity, "job_id", jobID, "err", err)
			httpError(w, 500, "internal error")
			return
		}
		if prev == "" {
			base.Outcome = internal.OutcomeJobNotFound
			a.recordEvent(r, base)
			slog.Warn("Job not found on completed event", "entity", entity, "job_id", jobID)
			_, _ = w.Write([]byte("Job " + i64s(jobID) + " not found."))
			return
		}
		base.Outcome = internal.OutcomeJobMarkedCompleted
		a.recordEvent(r, base)
		slog.Info("Job marked completed", "entity", entity, "job_id", jobID, "prev_status", prev)
		_, _ = w.Write([]byte("Job " + i64s(jobID) + " completed (was " + prev + ")."))
	}
}

// jsonStrings best-effort converts payload labels (which can be `null` or
// `[...strings]`) into a Go []string.
func jsonStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// asInt64 reads a JSON number (float64) from a decoded map. Returns 0 on absence.
func asInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func i64s(v int64) string { return strconv.FormatInt(v, 10) }
