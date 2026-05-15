package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riseproject-dev/riscv-runner-app/container-go/internal"
)

// TestWorkers_PaginationAndLinkHeader locks invariant caf0e8a: /workers.json
// emits the GitHub-style Link header with rel="next"/"prev".
func TestWorkers_PaginationAndLinkHeader(t *testing.T) {
	app, db, _, _ := schedTestApp()
	for i := 0; i < 250; i++ {
		db.Workers = append(db.Workers, internal.Worker{PodName: "p", Status: "completed"})
	}

	// Reset GetAllWorkers paging using a shim that respects perPage so total > perPage.
	db.Workers = db.Workers[:50] // We rely on FakeDB returning total=len(Workers) for any page.

	// Page 0 of 50 with per_page=10 → expect next + last links, no prev/first.
	r := httptest.NewRequest("GET", "/workers.json?per_page=10&page=0", nil)
	w := httptest.NewRecorder()
	app.handleWorkers(w, r)
	link := w.Header().Get("Link")
	if !strings.Contains(link, `rel="next"`) || !strings.Contains(link, `rel="last"`) {
		t.Fatalf("page 0 link header missing next/last: %q", link)
	}
	if strings.Contains(link, `rel="prev"`) || strings.Contains(link, `rel="first"`) {
		t.Fatalf("page 0 link header should not contain prev/first: %q", link)
	}

	// Page 2 of 50 with per_page=10 → expect both directions.
	r2 := httptest.NewRequest("GET", "/workers.json?per_page=10&page=2", nil)
	w2 := httptest.NewRecorder()
	app.handleWorkers(w2, r2)
	link2 := w2.Header().Get("Link")
	for _, rel := range []string{`rel="first"`, `rel="prev"`, `rel="next"`, `rel="last"`} {
		if !strings.Contains(link2, rel) {
			t.Errorf("middle page link header missing %s: %q", rel, link2)
		}
	}

	// Page 4 (final) of 50 with per_page=10 → only prev/first.
	r3 := httptest.NewRequest("GET", "/workers.json?per_page=10&page=4", nil)
	w3 := httptest.NewRecorder()
	app.handleWorkers(w3, r3)
	link3 := w3.Header().Get("Link")
	if !strings.Contains(link3, `rel="prev"`) || !strings.Contains(link3, `rel="first"`) {
		t.Errorf("last page link header missing prev/first: %q", link3)
	}
	if strings.Contains(link3, `rel="next"`) {
		t.Errorf("last page link header should not contain next: %q", link3)
	}
}

// TestHandlers_HealthOK is a smoke test for /health.
func TestHandlers_HealthOK(t *testing.T) {
	app, _, _, _ := schedTestApp()
	r := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	app.handleHealth(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("health response: %d %q", w.Code, w.Body.String())
	}
}
