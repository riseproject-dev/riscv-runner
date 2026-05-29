// SPDX-License-Identifier: MIT

package main

import (
	"github.com/riseproject-dev/riscv-runner/container/internal"
)

// senderDropKeys / repoDropKeys / repoOwnerDropKeys / orgDropKeys /
// jobDropKeys list the fields trimmed off workflow_job payloads before
// they hit installation_events. The lists are large but stable — drop only
// what's enumerated here, so future GitHub additions show up in the row by
// default. Invariant f264661.
var senderDropKeys = stringSet(
	"url", "html_url",
	"gists_url", "repos_url", "avatar_url", "events_url", "starred_url",
	"followers_url", "following_url", "organizations_url",
	"subscriptions_url", "received_events_url",
)

var repoDropKeys = stringSet(
	"url", "license",
	"git_url", "ssh_url", "svn_url", "html_url",
	"keys_url", "tags_url", "blobs_url", "clone_url", "forks_url",
	"hooks_url", "pulls_url", "teams_url", "trees_url", "events_url",
	"issues_url", "labels_url", "merges_url", "mirror_url", "archive_url",
	"commits_url", "compare_url", "branches_url", "comments_url",
	"contents_url", "git_refs_url", "git_tags_url", "releases_url",
	"statuses_url", "assignees_url", "downloads_url", "languages_url",
	"milestones_url", "stargazers_url", "deployments_url",
	"git_commits_url", "subscribers_url", "contributors_url",
	"issue_events_url", "subscription_url", "collaborators_url",
	"issue_comment_url", "notifications_url",
)

var repoOwnerDropKeys = senderDropKeys

var orgDropKeys = stringSet(
	"url",
	"hooks_url", "repos_url", "avatar_url", "events_url", "issues_url",
	"members_url", "public_members_url",
)

var jobDropKeys = stringSet(
	"url", "run_url", "check_run_url",
	"steps",
)

func stringSet(keys ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

// trimWorkflowJobPayload returns a shallow-copy of payload with the noisy
// URL/license/steps fields removed. Preserves workflow_job.html_url.
func trimWorkflowJobPayload(payload map[string]any) map[string]any {
	out := shallowCopy(payload)
	if v, ok := out["sender"].(map[string]any); ok {
		out["sender"] = dropKeys(v, senderDropKeys)
	}
	if v, ok := out["repository"].(map[string]any); ok {
		repo := shallowCopy(v)
		if owner, ok := repo["owner"].(map[string]any); ok {
			repo["owner"] = dropKeys(owner, repoOwnerDropKeys)
		}
		out["repository"] = dropKeys(repo, repoDropKeys)
	}
	if v, ok := out["organization"].(map[string]any); ok {
		out["organization"] = dropKeys(v, orgDropKeys)
	}
	if v, ok := out["workflow_job"].(map[string]any); ok {
		out["workflow_job"] = dropKeys(v, jobDropKeys)
	}
	return out
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func dropKeys(m map[string]any, drop map[string]struct{}) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if _, ok := drop[k]; !ok {
			out[k] = v
		}
	}
	return out
}

// matchLabelsToK8s maps a job's labels to (pool, image). Returns
// ("", "", false) when no rule matches — caller emits IGNORED_NO_LABEL.
func matchLabelsToK8s(cfg internal.Config, orgID int64, repoFullName string, labels []string) (pool, image string, ok bool) {
	isGGMLScope := orgID == internal.GGMLOrgID ||
		(orgID == internal.RiseprojectDevOrgID &&
			(repoFullName == "riseproject-dev/llama.cpp" || repoFullName == "riseproject-dev/llama.cpp-validation"))
	if isGGMLScope {
		if len(labels) == 1 && labels[0] == "ubuntu-24.04-riscv" {
			return "cloudv10x-jupiter", cfg.ImageUbuntu24, true
		}
		return "", "", false
	}

	isMengZhuoScope := orgID == internal.MengZhuoUserID
	if isMengZhuoScope {
		if len(labels) == 1 && labels[0] == "ubuntu-24.04-riscv" {
			return "cloudv10x-jupiter", cfg.ImageUbuntu24, true
		}
	}

	if len(labels) == 1 && labels[0] == "ubuntu-24.04-riscv" {
		return "scw-em-rv1", cfg.ImageUbuntu24, true
	}
	return "", "", false
}
