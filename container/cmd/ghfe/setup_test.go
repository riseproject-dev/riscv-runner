// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riseproject-dev/riscv-runner/container/internal"
	"github.com/riseproject-dev/riscv-runner/container/internal/testutil"
)

func newSetupApp(gh *testutil.FakeGH) *App {
	return &App{
		Config: internal.Config{Prod: false, WebhookSecret: "x"},
		DB:     testutil.NewFakeDB(),
		GH:     gh,
	}
}

func TestSetup_MissingInstallationID(t *testing.T) {
	app := newSetupApp(&testutil.FakeGH{})
	w := httptest.NewRecorder()
	app.handleSetupOrg(w, httptest.NewRequest("GET", "/setup/org", nil))
	if w.Code != 400 {
		t.Fatalf("status=%d want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Missing installation id") {
		t.Errorf("missing-id template not rendered:\n%s", w.Body.String())
	}
}

func TestSetup_NotFoundRendersWrongApp(t *testing.T) {
	gh := &testutil.FakeGH{
		OnGetInstallation: func(int64, internal.EntityType) (internal.Installation, error) {
			return internal.Installation{}, &internal.GitHubAPIError{StatusCode: 404, Message: "not found"}
		},
	}
	app := newSetupApp(gh)
	w := httptest.NewRecorder()
	app.handleSetupOrg(w, httptest.NewRequest("GET", "/setup/org?installation_id=42", nil))
	if w.Code != 404 {
		t.Fatalf("status=%d want 404", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Installation not found for this app") {
		t.Errorf("wrongApp template not rendered:\n%s", body)
	}
	if !strings.Contains(body, "<code>42</code>") {
		t.Errorf("installation id not echoed:\n%s", body)
	}
}

func TestSetup_UpstreamErrorRenders502(t *testing.T) {
	gh := &testutil.FakeGH{
		OnGetInstallation: func(int64, internal.EntityType) (internal.Installation, error) {
			return internal.Installation{}, &internal.GitHubAPIError{StatusCode: 500, Message: "boom"}
		},
	}
	app := newSetupApp(gh)
	w := httptest.NewRecorder()
	app.handleSetupOrg(w, httptest.NewRequest("GET", "/setup/org?installation_id=42", nil))
	if w.Code != 502 {
		t.Fatalf("status=%d want 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Something went wrong") {
		t.Errorf("upstreamError template not rendered:\n%s", w.Body.String())
	}
}

func TestSetup_InstalledOK(t *testing.T) {
	gh := &testutil.FakeGH{
		OnGetInstallation: func(_ int64, _ internal.EntityType) (internal.Installation, error) {
			return internal.Installation{Account: internal.InstallAccount{Type: "Organization", Login: "pytorch"}}, nil
		},
	}
	app := newSetupApp(gh)
	w := httptest.NewRecorder()
	app.handleSetupOrg(w, httptest.NewRequest("GET", "/setup/org?installation_id=42", nil))
	if w.Code != 200 {
		t.Fatalf("status=%d want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "All set, pytorch!") {
		t.Errorf("installedOK template not rendered:\n%s", body)
	}
}

func TestSetup_WrongType_PersonalOnOrg(t *testing.T) {
	gh := &testutil.FakeGH{
		OnGetInstallation: func(_ int64, _ internal.EntityType) (internal.Installation, error) {
			return internal.Installation{Account: internal.InstallAccount{Type: "User", Login: "alice"}}, nil
		},
	}
	app := newSetupApp(gh)
	w := httptest.NewRecorder()
	app.handleSetupOrg(w, httptest.NewRequest("GET", "/setup/org?installation_id=42", nil))
	if w.Code != 400 {
		t.Fatalf("status=%d want 400", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "organization app on a personal account") {
		t.Errorf("wrongType template (org branch) not rendered:\n%s", body)
	}
	if !strings.Contains(body, "<code>alice</code>") {
		t.Errorf("login not echoed:\n%s", body)
	}
}

func TestSetup_WrongType_OrgOnPersonal(t *testing.T) {
	gh := &testutil.FakeGH{
		OnGetInstallation: func(_ int64, _ internal.EntityType) (internal.Installation, error) {
			return internal.Installation{Account: internal.InstallAccount{Type: "Organization", Login: "acme"}}, nil
		},
	}
	app := newSetupApp(gh)
	w := httptest.NewRecorder()
	app.handleSetupPersonal(w, httptest.NewRequest("GET", "/setup/personal?installation_id=42", nil))
	if w.Code != 400 {
		t.Fatalf("status=%d want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "personal app on an organization") {
		t.Errorf("wrongType template (user branch) not rendered:\n%s", w.Body.String())
	}
}

func TestSetup_HTMLEscapingOnLogin(t *testing.T) {
	// Confirms html/template auto-escapes user-controlled fields.
	gh := &testutil.FakeGH{
		OnGetInstallation: func(_ int64, _ internal.EntityType) (internal.Installation, error) {
			return internal.Installation{Account: internal.InstallAccount{Type: "Organization", Login: "<script>x</script>"}}, nil
		},
	}
	app := newSetupApp(gh)
	w := httptest.NewRecorder()
	app.handleSetupOrg(w, httptest.NewRequest("GET", "/setup/org?installation_id=42", nil))
	if strings.Contains(w.Body.String(), "<script>x</script>") {
		t.Errorf("raw script tag rendered without escaping:\n%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "&lt;script&gt;") {
		t.Errorf("expected escaped <script>:\n%s", w.Body.String())
	}
}

// _ keeps the errors import live for the GitHubAPIError sentinel.
var _ = errors.New
