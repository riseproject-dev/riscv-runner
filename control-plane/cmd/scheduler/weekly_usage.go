// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
)

// weeklyUsageExcludedEntity is dropped from the second stacked chart per
// the standing request: riseproject-dev dwarfs the other entities and
// hides their trends.
const weeklyUsageExcludedEntity = "riseproject-dev"

// weeklyUsageTopEntities caps how many entities get their own series in the
// stacked charts (color-blind-safe categorical palette tops out at 8 slots,
// invariant documented in the dataviz skill). The rest fold into "Other".
const weeklyUsageTopEntities = 7

// weeklyUsagePivot is GetWeeklyEntityUsage's rows reshaped for pivoting:
// week x entity_name, with total_duration_minutes and job_count per cell.
type weeklyUsagePivot struct {
	Weeks    []time.Time
	Entities []string
	Duration map[time.Time]map[string]int64
	Jobs     map[time.Time]map[string]int64
}

func buildWeeklyUsagePivot(rows []internal.WeeklyEntityUsage) weeklyUsagePivot {
	weekSet := map[time.Time]bool{}
	entitySet := map[string]bool{}
	duration := map[time.Time]map[string]int64{}
	jobs := map[time.Time]map[string]int64{}
	for _, r := range rows {
		weekSet[r.Week] = true
		entitySet[r.EntityName] = true
		if duration[r.Week] == nil {
			duration[r.Week] = map[string]int64{}
			jobs[r.Week] = map[string]int64{}
		}
		duration[r.Week][r.EntityName] += r.TotalDurationMinutes
		jobs[r.Week][r.EntityName] += r.JobCount
	}

	weeks := make([]time.Time, 0, len(weekSet))
	for wk := range weekSet {
		weeks = append(weeks, wk)
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].Before(weeks[j]) })

	entities := make([]string, 0, len(entitySet))
	for e := range entitySet {
		entities = append(entities, e)
	}

	p := weeklyUsagePivot{Weeks: weeks, Entities: entities, Duration: duration, Jobs: jobs}
	sort.Slice(p.Entities, func(i, j int) bool {
		ti, tj := p.entityDurationTotal(p.Entities[i]), p.entityDurationTotal(p.Entities[j])
		if ti != tj {
			return ti > tj
		}
		return p.Entities[i] < p.Entities[j]
	})
	return p
}

func (p weeklyUsagePivot) entityDurationTotal(e string) int64 {
	var sum int64
	for _, wk := range p.Weeks {
		sum += p.Duration[wk][e]
	}
	return sum
}

func (p weeklyUsagePivot) entityJobTotal(e string) int64 {
	var sum int64
	for _, wk := range p.Weeks {
		sum += p.Jobs[wk][e]
	}
	return sum
}

func (p weeklyUsagePivot) weekDurationTotal(wk time.Time) int64 {
	var sum int64
	for _, e := range p.Entities {
		sum += p.Duration[wk][e]
	}
	return sum
}

func (p weeklyUsagePivot) weekJobTotal(wk time.Time) int64 {
	var sum int64
	for _, e := range p.Entities {
		sum += p.Jobs[wk][e]
	}
	return sum
}

func (p weeklyUsagePivot) grandDuration() int64 {
	var sum int64
	for _, wk := range p.Weeks {
		sum += p.weekDurationTotal(wk)
	}
	return sum
}

func (p weeklyUsagePivot) grandJobs() int64 {
	var sum int64
	for _, wk := range p.Weeks {
		sum += p.weekJobTotal(wk)
	}
	return sum
}

// renderWeeklyUsagePivotTable renders one merged week x entity_name table:
// each entity gets a Duration/Jobs/Avg column triplet, plus a SUM(Duration)
// and SUM(Jobs) grand-total column and row. Avg carries no total (averaging
// averages is meaningless), so its total-row cell is left blank. Entities
// are already ordered by SUM(total_duration_minutes) DESC (see
// buildWeeklyUsagePivot).
func renderWeeklyUsagePivotTable(p weeklyUsagePivot) string {
	var b strings.Builder
	b.WriteString("<h2>Weekly usage</h2>\n<div class=\"table-scroll\"><table class=\"pivot pivot-merged\">\n<thead>\n")

	b.WriteString("<tr><th rowspan=\"2\">Week</th>")
	for _, e := range p.Entities {
		fmt.Fprintf(&b, "<th colspan=\"3\" class=\"group-start\">%s</th>", html.EscapeString(e))
	}
	b.WriteString("<th colspan=\"2\" class=\"total-col\">Total</th></tr>\n<tr>")
	for range p.Entities {
		b.WriteString("<th class=\"group-start\">Duration</th><th>Jobs</th><th>Avg</th>")
	}
	b.WriteString("<th class=\"total-col\">Duration</th><th>Jobs</th></tr>\n</thead>\n<tbody>\n")

	for _, wk := range p.Weeks {
		fmt.Fprintf(&b, "<tr><th>%s</th>", wk.Format("2006-01-02"))
		for _, e := range p.Entities {
			dur, jobs := p.Duration[wk][e], p.Jobs[wk][e]
			fmt.Fprintf(&b, "<td class=\"group-start\">%s</td><td>%s</td><td>%s</td>",
				formatDuration(dur), formatInt(jobs), formatAvgDuration(dur, jobs))
		}
		fmt.Fprintf(&b, "<td class=\"total-col\">%s</td><td>%s</td></tr>\n",
			formatDuration(p.weekDurationTotal(wk)), formatInt(p.weekJobTotal(wk)))
	}

	b.WriteString("<tr class=\"total-row\"><th>Total</th>")
	for _, e := range p.Entities {
		fmt.Fprintf(&b, "<td class=\"group-start\">%s</td><td>%s</td><td>–</td>",
			formatDuration(p.entityDurationTotal(e)), formatInt(p.entityJobTotal(e)))
	}
	fmt.Fprintf(&b, "<td class=\"total-col\">%s</td><td>%s</td></tr>\n",
		formatDuration(p.grandDuration()), formatInt(p.grandJobs()))

	b.WriteString("</tbody>\n</table></div>\n")
	return b.String()
}

func formatInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// formatDuration renders a minute count as "X days H hours M minutes",
// dropping leading zero units (a duration under an hour is just "M
// minutes"; under a day, "H hours M minutes").
func formatDuration(totalMinutes int64) string {
	if totalMinutes == 0 {
		return "0 minutes"
	}
	neg := totalMinutes < 0
	n := totalMinutes
	if neg {
		n = -n
	}
	days := n / (24 * 60)
	hours := (n / 60) % 24
	minutes := n % 60

	var parts []string
	if days > 0 {
		parts = append(parts, unitCount(days, "day"))
	}
	if days > 0 || hours > 0 {
		parts = append(parts, unitCount(hours, "hour"))
	}
	parts = append(parts, unitCount(minutes, "minute"))

	s := strings.Join(parts, " ")
	if neg {
		s = "-" + s
	}
	return s
}

func unitCount(n int64, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func formatAvgDuration(duration, jobs int64) string {
	if jobs == 0 {
		return "–"
	}
	return formatDuration(int64(math.Round(float64(duration) / float64(jobs))))
}

// weeklyUsageChartSeries is one chart line/area: a display name, the CSS
// class carrying its color (see weeklyUsageStyle), and per-week values
// aligned to weeklyUsageChartData.Weeks.
type weeklyUsageChartSeries struct {
	Name   string  `json:"name"`
	Class  string  `json:"class"`
	Values []int64 `json:"values"`
}

type weeklyUsageChartData struct {
	Weeks                            []string                 `json:"weeks"`
	DurationByEntity                 []weeklyUsageChartSeries `json:"durationByEntity"`
	DurationByEntityExRiseprojectDev []weeklyUsageChartSeries `json:"durationByEntityExRiseprojectDev"`
	TotalDuration                    []int64                  `json:"totalDuration"`
	TotalJobs                        []int64                  `json:"totalJobs"`
}

// buildWeeklyUsageChartData keeps the top weeklyUsageTopEntities (p.Entities
// is already ranked by total duration DESC, see buildWeeklyUsagePivot) as
// their own series, and folds the rest into "Other". Each entity's CSS
// class is fixed by its rank so it keeps the same color whether or not
// it's excluded from a given chart (color follows the entity, never its
// position within a filtered chart).
func buildWeeklyUsageChartData(p weeklyUsagePivot) weeklyUsageChartData {
	weeks := make([]string, len(p.Weeks))
	for i, wk := range p.Weeks {
		weeks[i] = wk.Format("2006-01-02")
	}

	ranked := p.Entities
	top := ranked
	if len(top) > weeklyUsageTopEntities {
		top = top[:weeklyUsageTopEntities]
	}
	topSet := make(map[string]bool, len(top))
	for _, e := range top {
		topSet[e] = true
	}

	buildSeries := func(exclude string) []weeklyUsageChartSeries {
		var series []weeklyUsageChartSeries
		for i, e := range top {
			class := fmt.Sprintf("s%d", i+1)
			if e == exclude {
				continue
			}
			values := make([]int64, len(p.Weeks))
			for wi, wk := range p.Weeks {
				values[wi] = p.Duration[wk][e]
			}
			series = append(series, weeklyUsageChartSeries{Name: e, Class: class, Values: values})
		}
		other := make([]int64, len(p.Weeks))
		hasOther := false
		for _, e := range ranked {
			if topSet[e] || e == exclude {
				continue
			}
			hasOther = true
			for wi, wk := range p.Weeks {
				other[wi] += p.Duration[wk][e]
			}
		}
		if hasOther {
			series = append(series, weeklyUsageChartSeries{Name: "Other", Class: "s-other", Values: other})
		}
		return series
	}

	totalDuration := make([]int64, len(p.Weeks))
	totalJobs := make([]int64, len(p.Weeks))
	for i, wk := range p.Weeks {
		totalDuration[i] = p.weekDurationTotal(wk)
		totalJobs[i] = p.weekJobTotal(wk)
	}

	return weeklyUsageChartData{
		Weeks:                            weeks,
		DurationByEntity:                 buildSeries(""),
		DurationByEntityExRiseprojectDev: buildSeries(weeklyUsageExcludedEntity),
		TotalDuration:                    totalDuration,
		TotalJobs:                        totalJobs,
	}
}

// writeWeeklyUsageHTML renders /stats/weekly-usage as one merged pivot
// table plus three charts fed by a JSON data island. Charting is
// hand-rolled SVG/JS rather than a CDN dependency so the page stays
// self-contained on an internal network.
func (a *App) writeWeeklyUsageHTML(w http.ResponseWriter, rows []internal.WeeklyEntityUsage) {
	suffix := "Prod"
	if !a.Config.Prod {
		suffix = "Staging"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if len(rows) == 0 {
		fmt.Fprintf(w, "<!DOCTYPE html>\n<title>Weekly Usage - %s</title>\n<pre>No usage found.</pre>", html.EscapeString(suffix))
		return
	}

	pivot := buildWeeklyUsagePivot(rows)
	table := renderWeeklyUsagePivotTable(pivot)

	chartData, err := json.Marshal(buildWeeklyUsageChartData(pivot))
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}

	fmt.Fprintf(w, "<!DOCTYPE html>\n<title>Weekly Usage - %s</title>\n", html.EscapeString(suffix))
	w.Write([]byte(weeklyUsageStyle))
	w.Write([]byte("<h1>Weekly Usage</h1>\n"))
	w.Write([]byte(table))
	w.Write([]byte(weeklyUsageChartsMarkup))
	fmt.Fprintf(w, "<script id=\"weekly-usage-data\" type=\"application/json\">%s</script>\n", chartData)
	w.Write([]byte(weeklyUsageScript))
}
