package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

var statusColors = map[string]string{
	"pending":   "#ccc504",
	"running":   "#2563eb",
	"completed": "#16a34a",
	"failed":    "#d90606",
}

func formatStatus(s string) string {
	color, ok := statusColors[s]
	if !ok {
		color = "#666"
	}
	return fmt.Sprintf(`<span style="color:%s">[%-9s]</span>`, color, s)
}

func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

// formatLabelsRaw renders a JSONB-encoded labels array as "[a, b]" or "<none>".
func formatLabelsRaw(raw string) string {
	var labels []string
	if err := json.Unmarshal([]byte(raw), &labels); err != nil || len(labels) == 0 {
		return "<none>"
	}
	return "[" + strings.Join(labels, ", ") + "]"
}

func formatLabels(raw json.RawMessage) string {
	return formatLabelsRaw(string(raw))
}

func renderJob(j internal.Job) string {
	status := formatStatus(j.Status)
	repo := j.RepoFullName
	htmlURL := ""
	if j.HTMLURL != nil {
		htmlURL = *j.HTMLURL
	}
	labels := formatLabels(j.JobLabels)
	pod := "<unknown pod>"
	if j.K8sPod != nil && *j.K8sPod != "" {
		pod = *j.K8sPod
	}
	link := fmt.Sprintf("%s#%d", repo, j.JobID)
	if htmlURL != "" {
		link = fmt.Sprintf(`<a href="%s">%s#%d</a>`, htmlURL, repo, j.JobID)
	}
	return fmt.Sprintf("%s  %s  %s  %s  %s", status, formatTimestamp(j.CreatedAt), labels, pod, link)
}

// renderWorker formats one worker row as lines (caller joins with newline
// + indent). Failure_info has two on-disk shapes (v1 message-only, v2
// structured) and we still surface both for old rows.
func (a *App) renderWorker(r *http.Request, w internal.Worker) []string {
	status := formatStatus(w.Status)
	labels := formatLabels(w.JobLabels)
	node := "<unknown node>"
	if w.K8sNode != nil && *w.K8sNode != "" {
		node = *w.K8sNode
	}
	lines := []string{fmt.Sprintf("%s  %s  %s  %s  (node: %s)",
		status, formatTimestamp(w.CreatedAt), labels, w.PodName, node)}

	if w.Status == "failed" && len(w.FailureInfo) > 0 {
		lines = append(lines, renderFailureInfo(w.FailureInfo)...)
	} else {
		lines = append(lines, a.renderLiveEvents(r.Context(), w.PodName)...)
	}
	return lines
}

// renderFailureInfo supports both v1 (legacy, just a message) and v2 shapes.
// Invariant b081af0.
func renderFailureInfo(raw json.RawMessage) []string {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil
	}
	var lines []string
	version, _ := generic["version"].(float64)
	if version == 1 {
		// v1 had no structured rendering.
	} else if reason, ok := generic["reason"].(string); ok && reason != "" {
		lines = append(lines, "  Reason: "+reason)
	}
	podReason, _ := generic["pod_reason"].(string)
	podMessage, _ := generic["pod_message"].(string)
	if podReason != "" || podMessage != "" {
		s := "  Pod: " + valueOr(podReason, "?")
		if podMessage != "" {
			s += "  " + podMessage
		}
		lines = append(lines, strings.TrimRight(s, " "))
	}
	if containers, ok := generic["containers"].(map[string]any); ok {
		for name, raw := range containers {
			c, _ := raw.(map[string]any)
			if c == nil {
				continue
			}
			exit := fmt.Sprintf("%v", c["exit_code"])
			cReason := valueOr(stringOr(c["reason"], ""), "?")
			cMessage := stringOr(c["message"], "")
			head := fmt.Sprintf("  Container %s: exit=%s  %s  %s", name, exit, cReason, cMessage)
			lines = append(lines, strings.TrimRight(head, " "))
			logs := stringOr(c["logs"], "")
			if logs != "" {
				for _, l := range strings.Split(logs, "\n") {
					lines = append(lines, "    | "+l)
				}
			}
		}
	}
	if events, ok := generic["events"].([]any); ok {
		for _, raw := range events {
			ev, _ := raw.(map[string]any)
			if ev == nil {
				continue
			}
			ts := stringOr(ev["last_seen"], stringOr(ev["first_seen"], "unknown"))
			lines = append(lines, fmt.Sprintf("  %s  [%s]  %s: %s",
				ts, stringOr(ev["type"], ""), stringOr(ev["reason"], ""), stringOr(ev["message"], "")))
		}
	}
	return lines
}

func (a *App) renderLiveEvents(ctx context.Context, podName string) []string {
	evs, err := a.K8s.GetPodEvents(ctx, podName)
	if err != nil {
		slog.Debug("failed to fetch pod events", "pod_name", podName, "err", err)
		return []string{"  Events: (error fetching)"}
	}
	if len(evs) == 0 {
		return []string{"  Events: (none)"}
	}
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		ts := "unknown"
		if ev.LastSeen != nil {
			ts = ev.LastSeen.Format("2006-01-02 15:04:05")
		} else if ev.FirstSeen != nil {
			ts = ev.FirstSeen.Format("2006-01-02 15:04:05")
		}
		out = append(out, fmt.Sprintf("  %s  [%s]  %s: %s", ts, ev.Type, ev.Reason, ev.Message))
	}
	return out
}

func valueOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func stringOr(v any, def string) string {
	if v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
