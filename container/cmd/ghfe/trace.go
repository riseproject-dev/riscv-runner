// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// requireTraceAuth returns true when the bearer matches TRACE_API_SECRET.
// On failure it writes the 401 response.
func (a *App) requireTraceAuth(w http.ResponseWriter, r *http.Request) bool {
	want := "Bearer " + a.Config.TraceSecret
	if r.Header.Get("Authorization") != want {
		httpError(w, 401, "Unauthorized")
		return false
	}
	return true
}

func (a *App) handleTraceEntity(w http.ResponseWriter, r *http.Request) {
	if !a.requireTraceAuth(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("entity_id"), 10, 64)
	if err != nil {
		httpError(w, 400, "Invalid entity_id")
		return
	}
	events, err := a.DB.GetEventsByEntityID(r.Context(), id)
	if err != nil {
		httpError(w, 500, "internal error")
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

func (a *App) handleTraceInstallation(w http.ResponseWriter, r *http.Request) {
	if !a.requireTraceAuth(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("installation_id"), 10, 64)
	if err != nil {
		httpError(w, 400, "Invalid installation_id")
		return
	}
	eid, ok, err := a.DB.GetEntityIDForInstallation(r.Context(), id)
	if err != nil {
		httpError(w, 500, "internal error")
		return
	}
	if !ok {
		httpError(w, 404, "Entity not found")
		return
	}
	events, err := a.DB.GetEventsByEntityID(r.Context(), eid)
	if err != nil {
		httpError(w, 500, "internal error")
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

func (a *App) handleTraceJob(w http.ResponseWriter, r *http.Request) {
	if !a.requireTraceAuth(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil {
		httpError(w, 400, "Invalid job_id")
		return
	}
	eid, ok, err := a.DB.GetEntityIDForJob(r.Context(), id)
	if err != nil {
		httpError(w, 500, "internal error")
		return
	}
	if !ok {
		httpError(w, 404, "Entity not found")
		return
	}
	events, err := a.DB.GetEventsByEntityID(r.Context(), eid)
	if err != nil {
		httpError(w, 500, "internal error")
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

func (a *App) handleTracePayload(w http.ResponseWriter, r *http.Request) {
	if !a.requireTraceAuth(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil {
		httpError(w, 400, "Invalid event_id")
		return
	}
	payload, err := a.DB.GetPayloadByID(r.Context(), id)
	if err != nil {
		httpError(w, 500, "internal error")
		return
	}
	if payload == nil {
		httpError(w, 404, "Payload not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// payload is already valid JSON — wrap as {"payload": <raw>}
	w.WriteHeader(200)
	_, _ = w.Write([]byte(`{"payload":`))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte(`}`))
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(body)
	_, _ = w.Write(b)
}
