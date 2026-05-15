package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.HandleFunc("GET /usage", a.handleUsage)
	mux.HandleFunc("GET /usage.json", a.handleUsage)
	mux.HandleFunc("GET /history", a.handleJobs)
	mux.HandleFunc("GET /history.json", a.handleJobs)
	mux.HandleFunc("GET /jobs", a.handleJobs)
	mux.HandleFunc("GET /jobs.json", a.handleJobs)
	mux.HandleFunc("GET /workers", a.handleWorkers)
	mux.HandleFunc("GET /workers.json", a.handleWorkers)
	return mux
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func wantsJSON(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, ".json")
}

// --- /usage ---

func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	jobs, workers, err := a.DB.GetActiveJobsAndWorkers(r.Context())
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, map[string]any{"jobs": jobs, "workers": workers})
		return
	}
	// HTML view, grouped by (entity_id, job_labels)
	type group struct {
		EntityName string
		K8sPool    string
		Jobs       []internal.Job
		Workers    []internal.Worker
	}
	type key struct {
		EntityID int64
		Labels   string
	}
	groups := map[key]*group{}
	for _, j := range jobs {
		k := key{j.EntityID, string(j.JobLabels)}
		g, ok := groups[k]
		if !ok {
			g = &group{EntityName: j.EntityName, K8sPool: j.K8sPool}
			groups[k] = g
		}
		g.Jobs = append(g.Jobs, j)
	}
	for _, wkr := range workers {
		k := key{wkr.EntityID, string(wkr.JobLabels)}
		g, ok := groups[k]
		if !ok {
			g = &group{EntityName: wkr.EntityName, K8sPool: wkr.K8sPool}
			groups[k] = g
		}
		g.Workers = append(g.Workers, wkr)
	}

	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].EntityID != keys[j].EntityID {
			return keys[i].EntityID < keys[j].EntityID
		}
		return keys[i].Labels < keys[j].Labels
	})

	var lines []string
	for _, k := range keys {
		g := groups[k]
		labelsDisplay := formatLabelsRaw(k.Labels)
		lines = append(lines, fmt.Sprintf("=== %s / %s (%s) ===",
			html.EscapeString(g.EntityName), labelsDisplay, html.EscapeString(g.K8sPool)))
		if len(g.Jobs) > 0 {
			lines = append(lines, fmt.Sprintf("  Jobs (%d):", len(g.Jobs)))
			sort.Slice(g.Jobs, func(i, j int) bool { return g.Jobs[i].CreatedAt.Before(g.Jobs[j].CreatedAt) })
			for _, j := range g.Jobs {
				lines = append(lines, "    - "+renderJob(j))
			}
		} else {
			lines = append(lines, "  Jobs: none")
		}
		if len(g.Workers) > 0 {
			lines = append(lines, fmt.Sprintf("  Workers (%d):", len(g.Workers)))
			sort.Slice(g.Workers, func(i, j int) bool { return g.Workers[i].CreatedAt.Before(g.Workers[j].CreatedAt) })
			for _, wkr := range g.Workers {
				rendered := a.renderWorker(r, wkr)
				lines = append(lines, "    - "+strings.Join(rendered, "\n      "))
			}
		} else {
			lines = append(lines, "  Workers: none")
		}
		lines = append(lines, "")
	}
	if len(lines) == 0 {
		lines = []string{"No active pools."}
	}
	a.writePre(w, "Usage", lines)
}

// --- /history and /jobs ---

func (a *App) handleJobs(w http.ResponseWriter, r *http.Request) {
	start, end, page, perPage, err := parsePageParams(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	jobs, total, err := a.DB.GetAllJobs(r.Context(), start, end, page, perPage)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if wantsJSON(r) {
		setLinkHeader(w, r, page, perPage, total, start, end)
		writeJSON(w, jobs)
		return
	}
	lines := make([]string, 0, len(jobs))
	for _, j := range jobs {
		lines = append(lines, renderJob(j))
	}
	if len(lines) == 0 {
		lines = []string{"No jobs found."}
	}
	a.writePre(w, "History", lines)
}

// --- /workers ---

func (a *App) handleWorkers(w http.ResponseWriter, r *http.Request) {
	start, end, page, perPage, err := parsePageParams(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	workers, total, err := a.DB.GetAllWorkers(r.Context(), start, end, page, perPage)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if wantsJSON(r) {
		setLinkHeader(w, r, page, perPage, total, start, end)
		writeJSON(w, workers)
		return
	}
	var lines []string
	for _, wkr := range workers {
		lines = append(lines, a.renderWorker(r, wkr)...)
	}
	if len(lines) == 0 {
		lines = []string{"No workers found."}
	}
	a.writePre(w, "Workers", lines)
}

// parsePageParams normalises start/end/page/per_page and returns them parsed,
// or a Go error suitable for an HTTP 400 message.
func parsePageParams(r *http.Request) (string, string, int, int, error) {
	q := r.URL.Query()
	start, err := parseDateParam(q.Get("start"))
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("invalid parameter start, must be YYYY-MM-DD")
	}
	end, err := parseDateParam(q.Get("end"))
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("invalid parameter end, must be YYYY-MM-DD")
	}
	page := 0
	if s := q.Get("page"); s != "" {
		page, err = strconv.Atoi(s)
		if err != nil || page < 0 {
			return "", "", 0, 0, fmt.Errorf("invalid parameter page, must be >= 0")
		}
	}
	perPage := 100
	if s := q.Get("per_page"); s != "" {
		perPage, err = strconv.Atoi(s)
		if err != nil || perPage <= 0 {
			return "", "", 0, 0, fmt.Errorf("invalid parameter per_page, must be > 0")
		}
	}
	return start, end, page, perPage, nil
}

// parseDateParam accepts YYYY-MM-DD or -Xd.
func parseDateParam(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if len(v) > 1 && v[0] == '-' && v[len(v)-1] == 'd' {
		n, err := strconv.Atoi(v[1 : len(v)-1])
		if err != nil {
			return "", err
		}
		return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02"), nil
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return "", err
	}
	return v, nil
}

// setLinkHeader builds the GitHub-style Link header for /jobs.json and
// /workers.json (invariant caf0e8a).
func setLinkHeader(w http.ResponseWriter, r *http.Request, page, perPage, total int, start, end string) {
	last := 0
	if total > 0 {
		last = (total - 1) / perPage
	}
	base := r.URL.Path
	q := func(p int) string {
		s := fmt.Sprintf("page=%d&per_page=%d", p, perPage)
		if start != "" {
			s += "&start=" + start
		}
		if end != "" {
			s += "&end=" + end
		}
		return s
	}
	var links []string
	if page > 0 {
		links = append(links,
			fmt.Sprintf(`<%s?%s>; rel="first"`, base, q(0)),
			fmt.Sprintf(`<%s?%s>; rel="prev"`, base, q(page-1)),
		)
	}
	if page < last {
		links = append(links,
			fmt.Sprintf(`<%s?%s>; rel="next"`, base, q(page+1)),
			fmt.Sprintf(`<%s?%s>; rel="last"`, base, q(last)),
		)
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(body)
	_, _ = w.Write(b)
}

// writePre wraps the rendered lines in <pre>. Lines are emitted verbatim,
// so each template MUST html.EscapeString any field that can carry
// untrusted content (webhook labels, k8s event/container messages, etc.).
func (a *App) writePre(w http.ResponseWriter, base string, lines []string) {
	suffix := "Prod"
	if !a.Config.Prod {
		suffix = "Staging"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<title>%s - %s</title><pre>%s</pre>",
		html.EscapeString(base), suffix, strings.Join(lines, "\n"))
}
