package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riseproject-dev/riscv-runner-app/container-go/internal"
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
