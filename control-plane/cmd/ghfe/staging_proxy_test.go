// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
	"github.com/riseproject-dev/riscv-runner/control-plane/internal/testutil"
)

// stubRT lets a test capture the request the staging client sent and
// return a canned response. The 502 path uses err != nil; the happy
// path returns resp with status copied from the test.
type stubRT struct {
	gotReq  *http.Request
	gotBody []byte
	resp    *http.Response
	err     error
}

func (s *stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.gotReq = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		s.gotBody = b
	}
	return s.resp, s.err
}

func prodAppWithProxy(rt *stubRT) (*App, *testutil.FakeDB) {
	db := testutil.NewFakeDB()
	cfg := internal.Config{
		Prod:          true,
		WebhookSecret: webhookSecret,
		StagingURL:    "https://staging.example/ghfe",
		ImageUbuntu24: "img24",
	}
	return &App{Config: cfg, DB: db, GH: &testutil.FakeGH{}, StagingProxy: &http.Client{Transport: rt}}, db
}

func stagingPayload() []byte {
	return mustJSON(map[string]any{
		"action":       "queued",
		"installation": map[string]any{"id": float64(1)},
		"repository": map[string]any{
			"id":        float64(2),
			"full_name": "riseproject-staging/riscv-runner-sample",
			"owner":     map[string]any{"id": float64(internal.RiseprojectStagingOrgID), "type": "Organization", "login": "riseproject-staging"},
		},
		"workflow_job": map[string]any{
			"id":       float64(7),
			"labels":   []any{"ubuntu-24.04-riscv"},
			"html_url": "https://gh/run/7",
		},
	})
}

// TestStagingProxy_HappyPath: prod ghfe receives a workflow_job for a repo
// listed under EntityConfigs.Staging, forwards body + all request headers
// to StagingURL, records exactly one proxied_to_staging row, never touches
// the jobs table, and relays the staging response (status + body + headers)
// back to GitHub verbatim.
func TestStagingProxy_HappyPath(t *testing.T) {
	rt := &stubRT{resp: &http.Response{
		StatusCode: 202,
		Header:     http.Header{"Content-Type": []string{"text/plain"}, "X-Staging-Echo": []string{"ok"}},
		Body:       io.NopCloser(strings.NewReader("staging-ok")),
	}}
	app, db := prodAppWithProxy(rt)

	body := stagingPayload()
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	r.Header.Set(internal.HookSignatureHeader, sig)
	r.Header.Set(internal.HookEventHeader, "workflow_job")
	r.Header.Set(internal.HookAppIDHeader, "2167633")
	r.Header.Set("X-GitHub-Delivery", "abc-123")
	r.Header.Set("User-Agent", "GitHub-Hookshot/abc")
	r.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	app.handleWebhook(w, r)

	if w.Code != 202 {
		t.Fatalf("status=%d want 202 (relayed)", w.Code)
	}
	if got := w.Body.String(); got != "staging-ok" {
		t.Errorf("body=%q want %q", got, "staging-ok")
	}
	if w.Header().Get("X-Staging-Echo") != "ok" {
		t.Errorf("missing relayed response header X-Staging-Echo, got=%v", w.Header())
	}
	if w.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("response Content-Type=%q want text/plain", w.Header().Get("Content-Type"))
	}

	if rt.gotReq == nil {
		t.Fatal("staging client never called")
	}
	if rt.gotReq.URL.String() != "https://staging.example/ghfe/" {
		t.Errorf("forwarded URL=%q", rt.gotReq.URL.String())
	}
	if rt.gotReq.Method != "POST" {
		t.Errorf("method=%q", rt.gotReq.Method)
	}
	if !bytes.Equal(rt.gotBody, body) {
		t.Errorf("forwarded body differs from original")
	}
	for _, h := range []string{internal.HookSignatureHeader, internal.HookEventHeader, internal.HookAppIDHeader, "X-Github-Delivery", "User-Agent", "Content-Type"} {
		if rt.gotReq.Header.Get(h) == "" {
			t.Errorf("forwarded request missing header %s", h)
		}
	}
	if rt.gotReq.Header.Get(internal.HookSignatureHeader) != sig {
		t.Errorf("forwarded signature mutated")
	}

	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeProxiedToStaging) {
		t.Fatalf("expected one proxied_to_staging row, got %+v", db.Events)
	}
	if len(db.Jobs) != 0 {
		t.Fatalf("prod must not store proxied jobs locally, got %d", len(db.Jobs))
	}
}

// TestStagingProxy_UpstreamFailure: a transport error from staging surfaces
// as 502 to GitHub (so it redelivers) and the proxied_to_staging row is
// still written -- the proxy attempt itself is the audit-worthy event.
func TestStagingProxy_UpstreamFailure(t *testing.T) {
	rt := &stubRT{err: errors.New("connection refused")}
	app, db := prodAppWithProxy(rt)

	body := stagingPayload()
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	r.Header.Set(internal.HookSignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set(internal.HookEventHeader, "workflow_job")
	r.Header.Set(internal.HookAppIDHeader, "2167633")

	w := httptest.NewRecorder()
	app.handleWebhook(w, r)

	if w.Code != 502 {
		t.Fatalf("status=%d want 502", w.Code)
	}
	if len(db.Events) != 1 || db.Events[0].Row.Outcome != string(internal.OutcomeProxiedToStaging) {
		t.Fatalf("expected proxied_to_staging audit row even on failure, got %+v", db.Events)
	}
}

// TestShouldProxyToStaging_Negatives pins the four ways the proxy does NOT fire.
func TestShouldProxyToStaging_Negatives(t *testing.T) {
	prod := internal.Config{Prod: true, StagingURL: "https://s"}
	riseDevEntity := internal.Entity{ID: internal.RiseprojectDevOrgID}
	riseStagingEntity := internal.Entity{ID: internal.RiseprojectStagingOrgID}
	unknownEntity := internal.Entity{ID: 999999}

	cases := []struct {
		name string
		cfg  internal.Config
		ent  internal.Entity
	}{
		{"staging instance", internal.Config{Prod: false, StagingURL: "https://s"}, riseStagingEntity},
		{"no staging url", internal.Config{Prod: true}, riseStagingEntity},
		{"entity not in config", prod, unknownEntity},
		{"entity in config but not a staging entity", prod, riseDevEntity},
	}
	for _, tc := range cases {
		if shouldProxyToStaging(tc.cfg, tc.ent) {
			t.Errorf("%s: expected no proxy", tc.name)
		}
	}

	if !shouldProxyToStaging(prod, riseStagingEntity) {
		t.Error("positive case: prod + staging entity should proxy")
	}
}
