// SPDX-License-Identifier: MIT

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riseproject-dev/riscv-runner/container/internal"
	"github.com/riseproject-dev/riscv-runner/container/internal/testutil"
)

func TestHandleHealth(t *testing.T) {
	app := &App{Config: internal.Config{}, DB: testutil.NewFakeDB(), GH: &testutil.FakeGH{}}
	r := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	app.handleHealth(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body=%q", w.Body.String())
	}
}

func TestRoutes_HealthAndSetupRegistered(t *testing.T) {
	app := &App{Config: internal.Config{}, DB: testutil.NewFakeDB(), GH: &testutil.FakeGH{}}
	mux := app.Routes()

	for _, c := range []struct {
		method, path string
		wantStatus   int
	}{
		{"GET", "/health", 200},
		{"GET", "/setup/org", 400},      // no installation_id → renderMissing
		{"GET", "/setup/personal", 400}, // ditto
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != c.wantStatus {
			t.Errorf("%s %s: status=%d want %d body=%q", c.method, c.path, w.Code, c.wantStatus, w.Body.String())
		}
	}
}

func TestHTTPError_StatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, "two-hundred"},
		{400, "client"},
		{500, "server"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		httpError(w, c.status, c.want)
		if w.Code != c.status {
			t.Errorf("status=%d want %d", w.Code, c.status)
		}
		if w.Body.String() != c.want {
			t.Errorf("body=%q want %q", w.Body.String(), c.want)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("content-type=%q", ct)
		}
	}
}

func TestWithPerfLog_EmitsOnlyWhenEnabled(t *testing.T) {
	app := &App{Config: internal.Config{}, DB: testutil.NewFakeDB(), GH: &testutil.FakeGH{}}

	// Handler that opts in
	hOn := app.withPerfLog(func(w http.ResponseWriter, r *http.Request) {
		enablePerfLog(r)
		w.WriteHeader(202)
		_, _ = w.Write([]byte("done"))
	})
	r := httptest.NewRequest("POST", "/foo", strings.NewReader(""))
	w := httptest.NewRecorder()
	hOn(w, r)
	if w.Code != 202 || w.Body.String() != "done" {
		t.Errorf("status=%d body=%q", w.Code, w.Body.String())
	}

	// Handler that does not opt in (covers the false-branch of the perf-log gate)
	hOff := app.withPerfLog(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("silent"))
	})
	r = httptest.NewRequest("POST", "/bar", strings.NewReader(""))
	w = httptest.NewRecorder()
	hOff(w, r)
	if w.Body.String() != "silent" {
		t.Errorf("body=%q", w.Body.String())
	}

	// /health is the noisy path that withPerfLog suppresses even when opt-in.
	hHealth := app.withPerfLog(func(w http.ResponseWriter, r *http.Request) {
		enablePerfLog(r)
		w.WriteHeader(200)
	})
	r = httptest.NewRequest("GET", "/health", nil)
	w = httptest.NewRecorder()
	hHealth(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestEnablePerfLog_NoContextIsNoop(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	enablePerfLog(r) // must not panic when no perfLogger is in context
}
