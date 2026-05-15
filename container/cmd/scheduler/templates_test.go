package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

// TestRenderWorker_RendersV1AndV2FailureInfo locks b081af0: render_worker
// produces text for both shapes — v1 has no reason line, v2 includes reason,
// pod_reason/message, container exit codes/logs, events.
func TestRenderWorker_RendersV1AndV2FailureInfo(t *testing.T) {
	app, _, _, _ := schedTestApp()

	v1 := json.RawMessage(`{"version":1,"message":"old"}`)
	w := internal.Worker{
		PodName:     "p1",
		Status:      "failed",
		FailureInfo: v1,
	}
	rendered := app.renderWorker(httptest.NewRequest("GET", "/", nil), w)
	joined := strings.Join(rendered, "\n")
	if strings.Contains(joined, "Reason:") {
		t.Errorf("v1 should not render a Reason: line:\n%s", joined)
	}

	v2 := json.RawMessage(`{
		"version": 2,
		"reason": "pod_failed",
		"pod_reason": "OOMKilled",
		"pod_message": "out of memory",
		"containers": {
			"runner": {"exit_code": 137, "reason": "OOMKilled", "message": "kill -9", "logs": "boom\nboom2"}
		},
		"events": [{"type":"Warning","reason":"Failed","message":"oops","last_seen":"2025-01-01"}]
	}`)
	w2 := internal.Worker{PodName: "p2", Status: "failed", FailureInfo: v2}
	rendered2 := app.renderWorker(httptest.NewRequest("GET", "/", nil), w2)
	joined2 := strings.Join(rendered2, "\n")
	wants := []string{"Reason: pod_failed", "Pod: OOMKilled", "Container runner: exit=137", "boom", "Failed: oops"}
	for _, want := range wants {
		if !strings.Contains(joined2, want) {
			t.Errorf("v2 output missing %q:\n%s", want, joined2)
		}
	}
}

// TestRenderJob_AllShapes covers renderJob across with-html-url / without /
// without-k8s-pod variants.
func TestRenderJob_AllShapes(t *testing.T) {
	htmlURL := "https://example.com/r"
	pod := "rise-pod-1"
	j := internal.Job{
		JobID:        1,
		Status:       "pending",
		RepoFullName: "acme/r",
		JobLabels:    []byte(`["ubuntu-24.04-riscv"]`),
		HTMLURL:      &htmlURL,
		K8sPod:       &pod,
		CreatedAt:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	s := renderJob(j)
	for _, want := range []string{`<a href="https://example.com/r">acme/r#1</a>`, "rise-pod-1", "[ubuntu-24.04-riscv]", "[pending"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}

	// Without html url / pod
	j2 := internal.Job{JobID: 2, Status: "completed", RepoFullName: "x/y", JobLabels: []byte(`[]`)}
	s2 := renderJob(j2)
	if strings.Contains(s2, "<a") {
		t.Errorf("should not render link when html_url is nil: %q", s2)
	}
	if !strings.Contains(s2, "<unknown pod>") {
		t.Errorf("should mark missing pod: %q", s2)
	}
}

// TestRenderLiveEvents covers the three branches: error, none, formatted.
func TestRenderLiveEvents(t *testing.T) {
	app, _, _, kube := schedTestApp()

	// Error branch
	kube.OnGetPodEvents = func(string) ([]internal.PodEvent, error) { return nil, errBoom }
	got := app.renderLiveEvents(context.Background(), "p")
	if len(got) != 1 || !strings.Contains(got[0], "error fetching") {
		t.Errorf("got %v", got)
	}

	// No events
	kube.OnGetPodEvents = func(string) ([]internal.PodEvent, error) { return nil, nil }
	got = app.renderLiveEvents(context.Background(), "p")
	if len(got) != 1 || !strings.Contains(got[0], "(none)") {
		t.Errorf("got %v", got)
	}

	// Formatted with LastSeen
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	kube.OnGetPodEvents = func(string) ([]internal.PodEvent, error) {
		return []internal.PodEvent{
			{Type: "Warning", Reason: "Pull", Message: "img", LastSeen: &now},
		}, nil
	}
	got = app.renderLiveEvents(context.Background(), "p")
	if len(got) != 1 || !strings.Contains(got[0], "Pull: img") {
		t.Errorf("got %v", got)
	}

	// FirstSeen fallback
	kube.OnGetPodEvents = func(string) ([]internal.PodEvent, error) {
		return []internal.PodEvent{
			{Type: "Normal", Reason: "Created", Message: "", FirstSeen: &now},
		}, nil
	}
	got = app.renderLiveEvents(context.Background(), "p")
	if len(got) != 1 || !strings.Contains(got[0], "2025-01-01") {
		t.Errorf("got %v", got)
	}

	// Both nil → "unknown"
	kube.OnGetPodEvents = func(string) ([]internal.PodEvent, error) {
		return []internal.PodEvent{{Type: "Normal", Reason: "x"}}, nil
	}
	got = app.renderLiveEvents(context.Background(), "p")
	if !strings.Contains(got[0], "unknown") {
		t.Errorf("got %v", got)
	}
}

// TestFormatHelpers covers formatStatus / formatTimestamp / formatLabels.
func TestFormatHelpers(t *testing.T) {
	if s := formatStatus("pending"); !strings.Contains(s, "#ccc504") {
		t.Errorf("pending color: %q", s)
	}
	if s := formatStatus("???"); !strings.Contains(s, "#666") {
		t.Errorf("default color: %q", s)
	}
	if formatTimestamp(time.Time{}) != "?" {
		t.Errorf("zero timestamp wrong")
	}
	if formatLabels(json.RawMessage(`null`)) != "<none>" {
		t.Errorf("null labels wrong")
	}
	if got := formatLabels(json.RawMessage(`["a","b"]`)); got != "[a, b]" {
		t.Errorf("labels: %q", got)
	}
}

// TestStringOr covers each branch.
func TestStringOr(t *testing.T) {
	if stringOr(nil, "d") != "d" {
		t.Error("nil default")
	}
	if stringOr("ok", "d") != "ok" {
		t.Error("string passthrough")
	}
	if stringOr(7, "d") != "7" {
		t.Error("fmt fallback")
	}
}

// TestWorkers_FieldNames locks invariant 1055cc8 — the JSON serialisation of
// internal.Worker must keep the field names UI consumers expect.
func TestWorkers_FieldNames(t *testing.T) {
	w := internal.Worker{PodName: "p", Status: "pending"}
	b, _ := json.Marshal(w)
	s := string(b)
	for _, want := range []string{
		`"pod_name"`,
		`"status"`,
		`"job_labels"`,
		`"k8s_pool"`,
		`"k8s_image"`,
		`"entity_id"`,
		`"entity_name"`,
		`"installation_id"`,
		`"created_at"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing field %s in %s", want, s)
		}
	}
}
