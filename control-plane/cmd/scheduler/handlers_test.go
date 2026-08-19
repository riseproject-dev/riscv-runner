// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
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

// TestRoutes_AllPathsServed asserts every scheduler route is wired.
func TestRoutes_AllPathsServed(t *testing.T) {
	app, _, _, _ := schedTestApp()
	mux := app.Routes()
	for _, path := range []string{"/health", "/usage", "/usage.json", "/history", "/history.json", "/jobs", "/jobs.json", "/workers", "/workers.json"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code >= 500 {
			t.Errorf("%s: status=%d body=%q", path, w.Code, w.Body.String())
		}
	}
}

// TestWantsJSON covers the suffix decision.
func TestWantsJSON(t *testing.T) {
	for _, c := range []struct {
		path string
		want bool
	}{
		{"/usage.json", true},
		{"/usage", false},
		{"/workers.json?page=1", true}, // request URL strips query before Path
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		if got := wantsJSON(r); got != c.want {
			t.Errorf("%s: got %v want %v", c.path, got, c.want)
		}
	}
}

// TestUsage_JSONReturnsActiveJobsAndWorkers covers the JSON branch.
func TestUsage_JSONReturnsActiveJobsAndWorkers(t *testing.T) {
	app, db, _, _ := schedTestApp()
	db.Jobs = []internal.Job{{JobID: 1, EntityID: 9, EntityName: "acme", EntityType: "Organization", JobLabels: []byte(`["x"]`), K8sPool: "scw"}}
	db.Workers = []internal.Worker{{PodName: "p", EntityID: 9, EntityName: "acme", EntityType: "Organization", JobLabels: []byte(`["x"]`), K8sPool: "scw"}}
	r := httptest.NewRequest("GET", "/usage.json", nil)
	w := httptest.NewRecorder()
	app.handleUsage(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var out struct {
		Jobs    []internal.Job    `json:"jobs"`
		Workers []internal.Worker `json:"workers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Jobs) != 1 || len(out.Workers) != 1 {
		t.Errorf("body=%s", w.Body.String())
	}
}

// TestUsage_HTMLGroupsAndOrdering covers the HTML branch including grouping.
func TestUsage_HTMLGroupsAndOrdering(t *testing.T) {
	app, db, _, _ := schedTestApp()
	now := time.Now().UTC()
	db.Jobs = []internal.Job{
		{JobID: 2, EntityID: 1, EntityName: "a", EntityType: "Organization", JobLabels: []byte(`["x"]`), K8sPool: "p1", CreatedAt: now.Add(-time.Minute)},
		{JobID: 1, EntityID: 1, EntityName: "a", EntityType: "Organization", JobLabels: []byte(`["x"]`), K8sPool: "p1", CreatedAt: now.Add(-2 * time.Minute)},
	}
	db.Workers = []internal.Worker{
		{PodName: "p1", EntityID: 1, EntityName: "a", EntityType: "Organization", JobLabels: []byte(`["x"]`), K8sPool: "p1", Status: "running", CreatedAt: now},
		{PodName: "p2", EntityID: 2, EntityName: "b", EntityType: "Organization", JobLabels: []byte(`["y"]`), K8sPool: "p2", Status: "completed", CreatedAt: now},
	}
	r := httptest.NewRequest("GET", "/usage", nil)
	w := httptest.NewRecorder()
	app.handleUsage(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"a /", "b /", "Jobs (2):", "Workers (1):", "p1", "p2"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in:\n%s", want, body)
		}
	}
	// Staging suffix
	if !strings.Contains(body, "Staging") {
		t.Errorf("expected Staging suffix: %s", body)
	}
}

// TestUsage_NoActive covers the empty branch.
func TestUsage_NoActive(t *testing.T) {
	app, _, _, _ := schedTestApp()
	r := httptest.NewRequest("GET", "/usage", nil)
	w := httptest.NewRecorder()
	app.handleUsage(w, r)
	if !strings.Contains(w.Body.String(), "No active pools.") {
		t.Errorf("body=%q", w.Body.String())
	}
}

// TestUsage_DBError covers the 500 branch.
func TestUsage_DBError(t *testing.T) {
	app, db, _, _ := schedTestApp()
	db.OnGetActiveJobsAndWorkers = func() ([]internal.Job, []internal.Worker, error) { return nil, nil, errBoom }
	r := httptest.NewRequest("GET", "/usage", nil)
	w := httptest.NewRecorder()
	app.handleUsage(w, r)
	if w.Code != 500 {
		t.Errorf("status=%d", w.Code)
	}
}

// TestHistory_RendersJobs covers /history HTML + the no-jobs branch.
func TestHistory_RendersJobs(t *testing.T) {
	app, db, _, _ := schedTestApp()
	db.Jobs = []internal.Job{{JobID: 7, Status: "completed", EntityName: "acme", RepoFullName: "acme/r", K8sPool: "scaleway-em-rv1"}}

	r := httptest.NewRequest("GET", "/history", nil)
	w := httptest.NewRecorder()
	app.handleJobs(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "acme") {
		t.Errorf("body=%q", w.Body.String())
	}

	// JSON branch
	r = httptest.NewRequest("GET", "/history.json", nil)
	w = httptest.NewRecorder()
	app.handleJobs(w, r)
	var out []internal.Job
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].JobID != 7 {
		t.Errorf("body=%s", w.Body.String())
	}

	// Empty rendered output → "No jobs found."
	db.Jobs = nil
	r = httptest.NewRequest("GET", "/history", nil)
	w = httptest.NewRecorder()
	app.handleJobs(w, r)
	if !strings.Contains(w.Body.String(), "No jobs found.") {
		t.Errorf("body=%q", w.Body.String())
	}

	// DB error → 500
	db.OnGetAllJobs = func(string, string, int, int) ([]internal.Job, int, error) { return nil, 0, errBoom }
	r = httptest.NewRequest("GET", "/jobs", nil)
	w = httptest.NewRecorder()
	app.handleJobs(w, r)
	if w.Code != 500 {
		t.Errorf("status=%d", w.Code)
	}
}

// TestWorkers_EmptyMessage covers the no-rows branch.
func TestWorkers_EmptyMessage(t *testing.T) {
	app, _, _, _ := schedTestApp()
	r := httptest.NewRequest("GET", "/workers", nil)
	w := httptest.NewRecorder()
	app.handleWorkers(w, r)
	if !strings.Contains(w.Body.String(), "No workers found.") {
		t.Errorf("body=%q", w.Body.String())
	}
}

// TestWorkers_DBError covers /workers 500.
func TestWorkers_DBError(t *testing.T) {
	app, db, _, _ := schedTestApp()
	db.OnGetAllWorkers = func(string, string, int, int) ([]internal.Worker, int, error) { return nil, 0, errBoom }
	r := httptest.NewRequest("GET", "/workers", nil)
	w := httptest.NewRecorder()
	app.handleWorkers(w, r)
	if w.Code != 500 {
		t.Errorf("status=%d", w.Code)
	}
}

// TestParsePageParams covers each error branch.
func TestParsePageParams(t *testing.T) {
	cases := []struct {
		q       string
		wantErr bool
	}{
		{"", false},
		{"start=2024-01-01&end=2024-01-02&page=2&per_page=20", false},
		{"start=-7d", false},
		{"start=garbage", true},
		{"end=garbage", true},
		{"page=-1", true},
		{"page=abc", true},
		{"per_page=0", true},
		{"per_page=xx", true},
		{"start=-Xd", true}, // bad int in -Xd
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/jobs?"+c.q, nil)
		_, _, _, _, err := parsePageParams(r)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.q, err, c.wantErr)
		}
	}
}

// TestJobs_InvalidParam triggers the 400 path through the handler.
func TestJobs_InvalidParam(t *testing.T) {
	app, _, _, _ := schedTestApp()
	r := httptest.NewRequest("GET", "/jobs?start=junk", nil)
	w := httptest.NewRecorder()
	app.handleJobs(w, r)
	if w.Code != 400 {
		t.Errorf("status=%d", w.Code)
	}
}

func TestWorkers_InvalidParam(t *testing.T) {
	app, _, _, _ := schedTestApp()
	r := httptest.NewRequest("GET", "/workers?page=abc", nil)
	w := httptest.NewRecorder()
	app.handleWorkers(w, r)
	if w.Code != 400 {
		t.Errorf("status=%d", w.Code)
	}
}

// TestWritePreProdSuffix covers the Prod branch of writePre.
func TestWritePreProdSuffix(t *testing.T) {
	app, _, _, _ := schedTestApp()
	app.Config.Prod = true
	w := httptest.NewRecorder()
	app.writePre(w, "Title", []string{"line1"})
	if !strings.Contains(w.Body.String(), "Prod</title>") {
		t.Errorf("body=%q", w.Body.String())
	}
}

var errBoom = &stubErr{"boom"}

type stubErr struct{ s string }

func (e *stubErr) Error() string { return e.s }
