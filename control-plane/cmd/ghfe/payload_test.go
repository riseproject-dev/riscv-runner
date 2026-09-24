// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"testing"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
)

// TestTrimWorkflowJobPayload_DropsURLsLicenseSteps locks down invariant
// f264661: ~70 *_url fields, `license`, and `steps[]` are dropped, but
// `workflow_job.html_url` is preserved.
func TestTrimWorkflowJobPayload_DropsURLsLicenseSteps(t *testing.T) {
	body := `{
		"sender": {"login":"u", "url":"x", "html_url":"x", "avatar_url":"x"},
		"organization": {"login":"o", "url":"x", "hooks_url":"x", "members_url":"x"},
		"repository": {
			"id": 1, "full_name": "o/r",
			"url":"x", "html_url":"x", "license":{"name":"GPL"}, "clone_url":"x", "events_url":"x",
			"owner": {"login":"o", "url":"x", "html_url":"x", "avatar_url":"x"}
		},
		"workflow_job": {
			"id": 99, "html_url": "https://example.com/runs/99",
			"url": "drop", "run_url": "drop", "check_run_url": "drop",
			"steps": [{"name":"s1"}, {"name":"s2"}],
			"labels": ["ubuntu-24.04-riscv"]
		}
	}`
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	trimmed := trimWorkflowJobPayload(payload)

	mustDrop := []struct{ parent, key string }{
		{"sender", "url"}, {"sender", "html_url"}, {"sender", "avatar_url"},
		{"organization", "url"}, {"organization", "hooks_url"},
		{"workflow_job", "url"}, {"workflow_job", "run_url"}, {"workflow_job", "check_run_url"}, {"workflow_job", "steps"},
	}
	for _, k := range mustDrop {
		parent, _ := trimmed[k.parent].(map[string]any)
		if _, present := parent[k.key]; present {
			t.Errorf("%s.%s should be dropped", k.parent, k.key)
		}
	}

	repo, _ := trimmed["repository"].(map[string]any)
	for _, k := range []string{"url", "html_url", "license", "clone_url", "events_url"} {
		if _, present := repo[k]; present {
			t.Errorf("repository.%s should be dropped", k)
		}
	}
	owner, _ := repo["owner"].(map[string]any)
	if _, present := owner["html_url"]; present {
		t.Errorf("repository.owner.html_url should be dropped")
	}
	if repo["full_name"] != "o/r" {
		t.Errorf("repository.full_name lost: %v", repo["full_name"])
	}

	job, _ := trimmed["workflow_job"].(map[string]any)
	if job["html_url"] != "https://example.com/runs/99" {
		t.Errorf("workflow_job.html_url not preserved: %v", job["html_url"])
	}
}

// TestMatchLabelsToK8s covers the org-specific ladder in match_labels_to_k8s.
func TestMatchLabelsToK8s(t *testing.T) {
	cfg := internal.Config{
		ImageUbuntu24: "img24",
		ImageUbuntu26: "img26",
	}

	sel := func(board, provider string) internal.NodeSelector {
		return internal.NodeSelector{Board: board, Provider: provider}
	}

	tests := []struct {
		name      string
		orgID     int64
		repo      string
		labels    []string
		wantSel   internal.NodeSelector
		wantImage string
		wantOK    bool
	}{
		{"general ubuntu-24", 999, "x/y", []string{internal.GitHubLabelUbuntu24}, sel(internal.BoardScalewayEMRV1, internal.ProviderScaleway), cfg.ImageUbuntu24, true},
		{"general ubuntu-26", 999, "x/y", []string{internal.GitHubLabelUbuntu26}, sel(internal.BoardSpacemitK3, internal.ProviderRISE), cfg.ImageUbuntu26, true},
		{"general rva23", 999, "x/y", []string{internal.GitHubLabelUbuntu24, internal.GitHubLabelRVA23}, sel(internal.BoardSpacemitK3, internal.ProviderRISE), cfg.ImageUbuntu24, true},
		{"general rva23 reversed order", 999, "x/y", []string{internal.GitHubLabelRVA23, internal.GitHubLabelUbuntu24}, sel(internal.BoardSpacemitK3, internal.ProviderRISE), cfg.ImageUbuntu24, true},
		{"general no labels", 999, "x/y", []string{}, internal.NodeSelector{}, "", false},
		{"general other", 999, "x/y", []string{"ubuntu-98.04-riscv"}, internal.NodeSelector{}, "", false},

		{"ggml ubuntu-24", internal.GGMLOrgID, "ggml/llama.cpp", []string{internal.GitHubLabelUbuntu24}, sel(internal.BoardSpacemitK1, internal.ProviderCloudV10x), cfg.ImageUbuntu24, true},
		{"ggml with extra label", internal.GGMLOrgID, "ggml/llama.cpp", []string{internal.GitHubLabelUbuntu24, "extra"}, internal.NodeSelector{}, "", false},
		{"ggml scope blocks ubuntu-26", internal.GGMLOrgID, "ggml/llama.cpp", []string{internal.GitHubLabelUbuntu26}, internal.NodeSelector{}, "", false},
		{"riseproject llama.cpp ubuntu-24", internal.RiseprojectDevOrgID, "riseproject-dev/llama.cpp", []string{internal.GitHubLabelUbuntu24}, sel(internal.BoardSpacemitK1, internal.ProviderCloudV10x), cfg.ImageUbuntu24, true},
		{"riseproject llama.cpp-validation ubuntu-24", internal.RiseprojectDevOrgID, "riseproject-dev/llama.cpp-validation", []string{internal.GitHubLabelUbuntu24}, sel(internal.BoardSpacemitK1, internal.ProviderCloudV10x), cfg.ImageUbuntu24, true},

		// Same board as ggml, different provider.
		{"mengzhuo ubuntu-24", internal.MengZhuoUserID, "mengzhuo/r", []string{internal.GitHubLabelUbuntu24}, sel(internal.BoardSpacemitK1, internal.ProviderMengZhuo), cfg.ImageUbuntu24, true},

		{"ruyiai xlarge", internal.RuyiAIOrgID, "ruyi/r", []string{internal.GitHubLabelUbuntu24, internal.GitHubLabelRVA23, internal.GitHubLabelXL}, sel(internal.BoardSpacemitV100, internal.ProviderISCAS), cfg.ImageUbuntu24, true},
		{"luhenry xlarge", internal.LuhenryUserID, "luhenry/r", []string{internal.GitHubLabelXL, internal.GitHubLabelRVA23, internal.GitHubLabelUbuntu24}, sel(internal.BoardSpacemitV100, internal.ProviderISCAS), cfg.ImageUbuntu24, true},
		{"ruyiai falls through to default", internal.RuyiAIOrgID, "ruyi/r", []string{internal.GitHubLabelUbuntu24}, sel(internal.BoardScalewayEMRV1, internal.ProviderScaleway), cfg.ImageUbuntu24, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, image, ok := matchLabelsToK8s(cfg, tc.orgID, tc.repo, tc.labels)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want=%v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got != tc.wantSel {
				t.Fatalf("selector=%v want=%v", got, tc.wantSel)
			}
			if image != tc.wantImage {
				t.Fatalf("image=%q want=%q", image, tc.wantImage)
			}
		})
	}
}
