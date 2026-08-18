// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
	"github.com/riseproject-dev/riscv-runner/control-plane/internal/testutil"
)

func newTraceApp() (*App, *testutil.FakeDB) {
	db := testutil.NewFakeDB()
	cfg := internal.Config{TraceSecret: "trace-secret"}
	return &App{Config: cfg, DB: db, GH: &testutil.FakeGH{}}, db
}

func authedReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer trace-secret")
	return r
}

func TestTrace_RequiresAuth(t *testing.T) {
	app, _ := newTraceApp()
	mux := app.Routes()
	for _, path := range []string{"/trace/entity/1", "/trace/installation/1", "/trace/job/1", "/trace/payload/1"} {
		r := httptest.NewRequest("GET", path, nil) // no Authorization
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Errorf("%s: status=%d want 401", path, w.Code)
		}
	}
}

func TestTrace_EntityOK(t *testing.T) {
	app, db := newTraceApp()
	db.OnGetEventsByEntityID = func(id int64) ([]internal.InstallationEvent, error) {
		return []internal.InstallationEvent{{ID: 1, Event: "ping", Outcome: "ok"}}, nil
	}
	r := authedReq("GET", "/trace/entity/42", "")
	r.SetPathValue("entity_id", "42")
	w := httptest.NewRecorder()
	app.handleTraceEntity(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Events []internal.InstallationEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 1 || out.Events[0].ID != 1 {
		t.Errorf("body=%s", w.Body.String())
	}
}

func TestTrace_EntityInvalidID(t *testing.T) {
	app, _ := newTraceApp()
	r := authedReq("GET", "/trace/entity/abc", "")
	r.SetPathValue("entity_id", "abc")
	w := httptest.NewRecorder()
	app.handleTraceEntity(w, r)
	if w.Code != 400 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestTrace_EntityDBError(t *testing.T) {
	app, db := newTraceApp()
	db.OnGetEventsByEntityID = func(int64) ([]internal.InstallationEvent, error) {
		return nil, errBoom
	}
	r := authedReq("GET", "/trace/entity/1", "")
	r.SetPathValue("entity_id", "1")
	w := httptest.NewRecorder()
	app.handleTraceEntity(w, r)
	if w.Code != 500 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestTrace_InstallationFlow(t *testing.T) {
	app, db := newTraceApp()
	db.OnGetEntityIDInstall = func(id int64) (int64, bool, error) { return 99, true, nil }
	db.OnGetEventsByEntityID = func(int64) ([]internal.InstallationEvent, error) {
		return []internal.InstallationEvent{{ID: 7}}, nil
	}
	r := authedReq("GET", "/trace/installation/3", "")
	r.SetPathValue("installation_id", "3")
	w := httptest.NewRecorder()
	app.handleTraceInstallation(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// Not found
	db.OnGetEntityIDInstall = func(int64) (int64, bool, error) { return 0, false, nil }
	r = authedReq("GET", "/trace/installation/4", "")
	r.SetPathValue("installation_id", "4")
	w = httptest.NewRecorder()
	app.handleTraceInstallation(w, r)
	if w.Code != 404 {
		t.Fatalf("expected 404 not_found, got %d", w.Code)
	}

	// DB error on lookup
	db.OnGetEntityIDInstall = func(int64) (int64, bool, error) { return 0, false, errBoom }
	r = authedReq("GET", "/trace/installation/5", "")
	r.SetPathValue("installation_id", "5")
	w = httptest.NewRecorder()
	app.handleTraceInstallation(w, r)
	if w.Code != 500 {
		t.Fatalf("expected 500, got %d", w.Code)
	}

	// Invalid id
	r = authedReq("GET", "/trace/installation/abc", "")
	r.SetPathValue("installation_id", "abc")
	w = httptest.NewRecorder()
	app.handleTraceInstallation(w, r)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	// Events lookup error after ok install
	db.OnGetEntityIDInstall = func(int64) (int64, bool, error) { return 99, true, nil }
	db.OnGetEventsByEntityID = func(int64) ([]internal.InstallationEvent, error) { return nil, errBoom }
	r = authedReq("GET", "/trace/installation/6", "")
	r.SetPathValue("installation_id", "6")
	w = httptest.NewRecorder()
	app.handleTraceInstallation(w, r)
	if w.Code != 500 {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestTrace_JobFlow(t *testing.T) {
	app, db := newTraceApp()
	db.OnGetEntityIDJob = func(int64) (int64, bool, error) { return 99, true, nil }
	db.OnGetEventsByEntityID = func(int64) ([]internal.InstallationEvent, error) {
		return nil, nil
	}
	r := authedReq("GET", "/trace/job/1", "")
	r.SetPathValue("job_id", "1")
	w := httptest.NewRecorder()
	app.handleTraceJob(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	db.OnGetEntityIDJob = func(int64) (int64, bool, error) { return 0, false, nil }
	r = authedReq("GET", "/trace/job/2", "")
	r.SetPathValue("job_id", "2")
	w = httptest.NewRecorder()
	app.handleTraceJob(w, r)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	db.OnGetEntityIDJob = func(int64) (int64, bool, error) { return 0, false, errBoom }
	r = authedReq("GET", "/trace/job/3", "")
	r.SetPathValue("job_id", "3")
	w = httptest.NewRecorder()
	app.handleTraceJob(w, r)
	if w.Code != 500 {
		t.Fatalf("expected 500, got %d", w.Code)
	}

	r = authedReq("GET", "/trace/job/x", "")
	r.SetPathValue("job_id", "x")
	w = httptest.NewRecorder()
	app.handleTraceJob(w, r)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	// 200 path with downstream events error → 500
	db.OnGetEntityIDJob = func(int64) (int64, bool, error) { return 99, true, nil }
	db.OnGetEventsByEntityID = func(int64) ([]internal.InstallationEvent, error) { return nil, errBoom }
	r = authedReq("GET", "/trace/job/7", "")
	r.SetPathValue("job_id", "7")
	w = httptest.NewRecorder()
	app.handleTraceJob(w, r)
	if w.Code != 500 {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestTrace_PayloadFlow(t *testing.T) {
	app, db := newTraceApp()

	// Payload found
	db.OnGetPayloadByID = func(int64) ([]byte, error) { return []byte(`{"k":"v"}`), nil }
	r := authedReq("GET", "/trace/payload/1", "")
	r.SetPathValue("event_id", "1")
	w := httptest.NewRecorder()
	app.handleTracePayload(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != `{"payload":{"k":"v"}}` {
		t.Errorf("body=%q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type=%q", ct)
	}

	// Payload missing → 404
	db.OnGetPayloadByID = func(int64) ([]byte, error) { return nil, nil }
	r = authedReq("GET", "/trace/payload/2", "")
	r.SetPathValue("event_id", "2")
	w = httptest.NewRecorder()
	app.handleTracePayload(w, r)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// DB error → 500
	db.OnGetPayloadByID = func(int64) ([]byte, error) { return nil, errBoom }
	r = authedReq("GET", "/trace/payload/3", "")
	r.SetPathValue("event_id", "3")
	w = httptest.NewRecorder()
	app.handleTracePayload(w, r)
	if w.Code != 500 {
		t.Fatalf("expected 500, got %d", w.Code)
	}

	// Invalid id
	r = authedReq("GET", "/trace/payload/abc", "")
	r.SetPathValue("event_id", "abc")
	w = httptest.NewRecorder()
	app.handleTracePayload(w, r)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

var errBoom = &stubErr{"boom"}

type stubErr struct{ s string }

func (e *stubErr) Error() string { return e.s }
