// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

// shouldProxyToStaging is true when this is the prod instance and the
// (entity, repo) pair is listed as a staging-test repo in EntityConfigs.
// Prod forwards those workflow_job webhooks to the staging ghfe so the
// staging environment can exercise a real repo end-to-end.
func shouldProxyToStaging(cfg internal.Config, entity internal.Entity, repoFullName string) bool {
	if !cfg.Prod || cfg.StagingURL == "" {
		return false
	}
	ec, ok := internal.EntityConfigs[entity.ID]
	if !ok || len(ec.Staging) == 0 {
		return false
	}
	repoName := repoFullName
	if i := strings.IndexByte(repoFullName, '/'); i >= 0 {
		repoName = repoFullName[i+1:]
	}
	for _, r := range ec.Staging {
		if r == repoName {
			return true
		}
	}
	return false
}

// proxyToStaging forwards the unmodified webhook body to the staging
// ghfe and relays its response back to GitHub. Caller MUST have already
// verified the HMAC signature so we don't amplify untrusted traffic.
//
// Errors return 502 so GitHub redelivers.
func (a *App) proxyToStaging(w http.ResponseWriter, r *http.Request, body []byte) error {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.Config.StagingURL+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	// Forward every request header verbatim. Staging needs the GitHub
	// signature/event/delivery headers to verify and trace; carrying the
	// rest is harmless and keeps the proxy behaviorally invisible.
	req.Header = r.Header.Clone()

	resp, err := a.StagingProxy.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Relay staging's response headers (mainly Content-Type and any
	// X-GitHub-* it sets) back to GitHub.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Warn("Failed to relay staging proxy response", "err", err)
	}
	return nil
}
