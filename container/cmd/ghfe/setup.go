package main

import (
	"bytes"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

//go:embed setup.gohtml
var setupTemplateFS embed.FS

var setupTemplates = template.Must(template.ParseFS(setupTemplateFS, "setup.gohtml"))

func (a *App) handleSetupOrg(w http.ResponseWriter, r *http.Request) {
	a.renderSetup(w, r, internal.EntityOrganization)
}

func (a *App) handleSetupPersonal(w http.ResponseWriter, r *http.Request) {
	a.renderSetup(w, r, internal.EntityUser)
}

// renderSetup is the post-install redirect target for the GitHub Apps.
// It validates the installation_id query param and renders a friendly HTML
// page describing success / wrong-app / wrong-account-type / upstream-error.
func (a *App) renderSetup(w http.ResponseWriter, r *http.Request, expected internal.EntityType) {
	instID := r.URL.Query().Get("installation_id")
	if instID == "" {
		renderMissing(w, 400)
		return
	}
	id, err := strconv.ParseInt(instID, 10, 64)
	if err != nil {
		renderMissing(w, 400)
		return
	}

	inst, err := a.GH.GetInstallation(r.Context(), id, expected)
	if err != nil {
		var apiErr *internal.GitHubAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			renderWrongApp(w, 404, expected, instID)
			return
		}
		slog.Error("Unexpected error fetching installation", "installation_id", instID, "err", err)
		renderUpstreamError(w, 502, apiStatus(err))
		return
	}

	if inst.Account.Type == string(expected) {
		renderInstalledOK(w, 200, expected, inst.Account.Login)
		return
	}

	e := internal.Entity{Type: internal.EntityType(inst.Account.Type), Name: inst.Account.Login, ID: inst.Account.ID}
	switch expected {
	case internal.EntityOrganization:
		slog.Info("Entity installed Personal Account app on Organization", "entity", e)
		renderWrongAppForType(w, 400, true, inst.Account.Login)
	case internal.EntityUser:
		slog.Info("Entity installed Organization app on Personal Account", "entity", e)
		renderWrongAppForType(w, 400, false, inst.Account.Login)
	default:
		panic("unhandled EntityType: " + string(expected))
	}
}

func apiStatus(err error) int {
	var apiErr *internal.GitHubAPIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// renderTemplate writes the named setup.gohtml block. Templates are parsed
// at init; the only failure mode is a programmer mismatch between the data
// struct and the template — surface that as a 500 with the error logged.
func renderTemplate(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := setupTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render setup template", "name", name, "err", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func renderMissing(w http.ResponseWriter, status int) {
	renderTemplate(w, status, "missing", struct {
		Title       string
		OrgURL      string
		PersonalURL string
	}{
		Title:       "RISE RISC-V Runners — Setup",
		OrgURL:      internal.OrgAppInstallURL,
		PersonalURL: internal.PersonalAppInstallURL,
	})
}

func renderWrongApp(w http.ResponseWriter, status int, expected internal.EntityType, installationID string) {
	var wrongAppName, rightURL, expectedHuman string
	switch expected {
	case internal.EntityOrganization:
		wrongAppName, rightURL, expectedHuman = "personal", internal.PersonalAppInstallURL, "organization"
	case internal.EntityUser:
		wrongAppName, rightURL, expectedHuman = "organization", internal.OrgAppInstallURL, "personal"
	default:
		panic("unhandled EntityType: " + string(expected))
	}
	renderTemplate(w, status, "wrongApp", struct {
		Title          string
		InstallationID string
		ExpectedHuman  string
		WrongAppName   string
		RightURL       string
	}{
		Title:          "RISE RISC-V Runners — Wrong app",
		InstallationID: installationID,
		ExpectedHuman:  expectedHuman,
		WrongAppName:   wrongAppName,
		RightURL:       rightURL,
	})
}

func renderUpstreamError(w http.ResponseWriter, status, upstreamStatus int) {
	renderTemplate(w, status, "upstreamError", struct {
		Title  string
		Status int
	}{
		Title:  "RISE RISC-V Runners — Setup error",
		Status: upstreamStatus,
	})
}

func renderInstalledOK(w http.ResponseWriter, status int, expected internal.EntityType, login string) {
	var kind string
	switch expected {
	case internal.EntityOrganization:
		kind = "organization"
	case internal.EntityUser:
		kind = "personal"
	default:
		panic("unhandled EntityType: " + string(expected))
	}
	renderTemplate(w, status, "installedOK", struct {
		Title string
		Login string
		Kind  string
	}{
		Title: "RISE RISC-V Runners — Installed",
		Login: login,
		Kind:  kind,
	})
}

func renderWrongAppForType(w http.ResponseWriter, status int, expectedOrg bool, login string) {
	otherURL := internal.PersonalAppInstallURL
	if !expectedOrg {
		otherURL = internal.OrgAppInstallURL
	}
	renderTemplate(w, status, "wrongType", struct {
		Title       string
		Login       string
		ExpectedOrg bool
		OtherURL    string
	}{
		Title:       "RISE RISC-V Runners — Wrong account type",
		Login:       login,
		ExpectedOrg: expectedOrg,
		OtherURL:    otherURL,
	})
}
