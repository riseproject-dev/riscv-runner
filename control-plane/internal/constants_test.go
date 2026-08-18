// SPDX-License-Identifier: MIT

package internal

import (
	"strings"
	"testing"
)

func TestOrDefault(t *testing.T) {
	if got := orDefault("", "fallback"); got != "fallback" {
		t.Errorf("empty: got %q", got)
	}
	if got := orDefault("v", "fallback"); got != "v" {
		t.Errorf("non-empty: got %q", got)
	}
}

func TestLoadConfig_AllRequiredPresentProd(t *testing.T) {
	env := map[string]string{
		"PROD":                       "true",
		"PROD_URL":                   "https://prod",
		"STAGING_URL":                "https://stg",
		"POSTGRES_URL":               "postgres://x",
		"K8S_KUBECONFIG":             "kube: yaml",
		"LOGLEVEL":                   "DEBUG",
		"TRACE_API_SECRET":           "tsec",
		"GHAPP_WEBHOOK_SECRET":       "wsec",
		"GHAPP_ORG_PRIVATE_KEY":      "okey",
		"GHAPP_PERSONAL_PRIVATE_KEY": "pkey",
	}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !cfg.Prod || cfg.PostgresSchema != "prod" {
		t.Errorf("prod path off: %+v", cfg)
	}
	if cfg.RunnerPrefix != "rise-riscv-runner-" {
		t.Errorf("runner prefix: %q", cfg.RunnerPrefix)
	}
	if !strings.Contains(cfg.ImageUbuntu24, "ubuntu-24.04-latest") {
		t.Errorf("image24: %q", cfg.ImageUbuntu24)
	}
	if !strings.Contains(cfg.ImageUbuntu26, "ubuntu-26.04-latest") {
		t.Errorf("image26: %q", cfg.ImageUbuntu26)
	}
	if cfg.LogLevel != "DEBUG" {
		t.Errorf("loglevel: %q", cfg.LogLevel)
	}
}

func TestLoadConfig_StagingDefaults(t *testing.T) {
	env := map[string]string{
		"PROD_URL":                   "https://prod",
		"STAGING_URL":                "https://stg",
		"POSTGRES_URL":               "postgres://x",
		"K8S_KUBECONFIG":             "kube: yaml",
		"TRACE_API_SECRET":           "tsec",
		"GHAPP_WEBHOOK_SECRET":       "wsec",
		"GHAPP_ORG_PRIVATE_KEY":      "okey",
		"GHAPP_PERSONAL_PRIVATE_KEY": "pkey",
	}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.Prod || cfg.PostgresSchema != "staging" {
		t.Errorf("expected staging: %+v", cfg)
	}
	if cfg.RunnerPrefix != "rise-riscv-runner-staging-" {
		t.Errorf("staging prefix: %q", cfg.RunnerPrefix)
	}
	if cfg.LogLevel != "INFO" {
		t.Errorf("default loglevel should be INFO: %q", cfg.LogLevel)
	}
	if !strings.Contains(cfg.ImageUbuntu24, "ubuntu-24.04-staging") {
		t.Errorf("staging image24: %q", cfg.ImageUbuntu24)
	}
}

func TestLoadConfig_MissingRequiredListed(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error")
	}
	if cfg.PostgresSchema != "" {
		t.Errorf("cfg should be zero: %+v", cfg)
	}
	for _, k := range []string{"PROD_URL", "STAGING_URL", "POSTGRES_URL", "K8S_KUBECONFIG", "TRACE_API_SECRET", "GHAPP_WEBHOOK_SECRET", "GHAPP_ORG_PRIVATE_KEY", "GHAPP_PERSONAL_PRIVATE_KEY"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("missing key %q not mentioned: %v", k, err)
		}
	}
}

func TestLoadConfigFromEnv_NoEnv(t *testing.T) {
	// Unset every required var so the env-backed loader fails cleanly.
	for _, k := range []string{"PROD_URL", "STAGING_URL", "POSTGRES_URL", "K8S_KUBECONFIG", "TRACE_API_SECRET", "GHAPP_WEBHOOK_SECRET", "GHAPP_ORG_PRIVATE_KEY", "GHAPP_PERSONAL_PRIVATE_KEY", "PROD", "LOGLEVEL"} {
		t.Setenv(k, "")
	}
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error with empty env")
	}
}
