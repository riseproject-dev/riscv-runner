// SPDX-License-Identifier: MIT

// Package internal hosts shared types, configuration, and clients
// used by both cmd/ghfe and cmd/scheduler.
package internal

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// App ids point at the two GitHub Apps. The URLs in the comments are
	// the user-facing install pages.
	GHAppOrgID      int64 = 2167633 // https://github.com/apps/rise-risc-v-runners
	GHAppPersonalID int64 = 3131217 // https://github.com/apps/rise-risc-v-runners-personal

	// Organization / user IDs we special-case. Get a new one with:
	//   gh api orgs/<orgname> --jq '.id'
	//   gh api users/<username> --jq '.id'
	RiseprojectDevOrgID     int64 = 152654596 // github.com/riseproject-dev
	RiseprojectStagingOrgID int64 = 277861810 // github.com/riseproject-staging
	PyTorchOrgID            int64 = 21003710  // github.com/pytorch
	GGMLOrgID               int64 = 134263123 // github.com/ggml-org (for llama.cpp)
	RuyiAIOrgID             int64 = 257673382 // github.com/RuyiAI-Stack
	LuhenryUserID           int64 = 660779    // github.com/luhenry
	MengZhuoUserID          int64 = 885662    // github.com/mengzhuo

	RunnerRegistry = "rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s"
	RunnerImage    = "riscv-runner"

	// Reconciliation timeouts. Each one watches a different failure mode:
	RunnerRegistrationTimeout = 120 * time.Second // pod Running but GH never sees the runner
	RunnerPendingTimeout      = 600 * time.Second // runner registered with GH but never picks up a job
	PodPendingTimeout         = 600 * time.Second // pod stuck Pending (no capacity, image pull, etc.)
	PodDeleteGrace            = 6 * time.Hour     // keep terminal pods around so operators can still kubectl logs them
	JobStuckQueuedMinAge      = 10 * time.Minute  // minimum job age before sync_jobs probes the parent run for a stuck-queued condition

	PollInterval = 15 * time.Second

	PostgresMaxConn = 10
	HTTPPort        = 8080

	HookSignatureHeader = "X-Hub-Signature-256"
	HookEventHeader     = "X-Github-Event"
	HookAppIDHeader     = "X-GitHub-Hook-Installation-Target-Id"

	OrgAppInstallURL      = "https://github.com/apps/rise-risc-v-runners/installations/new"
	PersonalAppInstallURL = "https://github.com/apps/rise-risc-v-runners-personal/installations/new"
)

// EntityConfig caps how many concurrent workers an entity may have,
// and lists staging repos that proxy through to the staging environment.
type EntityConfig struct {
	MaxWorkers *int // nil = unlimited
	Staging    bool
}

// EntityConfigs is keyed by entity id (org id or user id).
var EntityConfigs = map[int64]EntityConfig{
	RiseprojectDevOrgID:     {MaxWorkers: nil},
	RiseprojectStagingOrgID: {MaxWorkers: nil, Staging: true},
	PyTorchOrgID:            {MaxWorkers: intPtr(20)},
	GGMLOrgID:               {MaxWorkers: intPtr(20)},
	LuhenryUserID:           {MaxWorkers: nil},
}

// DefaultMaxWorkers applies when an entity has no entry in EntityConfigs.
const DefaultMaxWorkers = 20

func intPtr(v int) *int { return &v }

// Config is the runtime configuration assembled from environment variables.
// Pass it by value; do not mutate after Load.
type Config struct {
	Prod          bool
	ProdURL       string
	StagingURL    string
	PostgresURL   string
	K8sKubeYAML   string
	LogLevel      string
	TraceSecret   string
	WebhookSecret string

	GHAppOrgKey      string
	GHAppPersonalKey string

	// Derived
	PostgresSchema string
	RunnerGroup    string
	RunnerPrefix   string
	ImageUbuntu24  string
	ImageUbuntu26  string
}

// LoadConfig reads required env vars and derives schema-dependent values.
// Returns an error listing every missing required variable; callers should
// exit with status 2 on a load failure.
func LoadConfig(getenv func(string) string) (Config, error) {
	var missing []string
	req := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}

	cfg := Config{
		Prod:             strings.EqualFold(getenv("PROD"), "true"),
		ProdURL:          req("PROD_URL"),
		StagingURL:       req("STAGING_URL"),
		PostgresURL:      req("POSTGRES_URL"),
		K8sKubeYAML:      req("K8S_KUBECONFIG"),
		LogLevel:         orDefault(getenv("LOGLEVEL"), "INFO"),
		TraceSecret:      req("TRACE_API_SECRET"),
		WebhookSecret:    req("GHAPP_WEBHOOK_SECRET"),
		GHAppOrgKey:      req("GHAPP_ORG_PRIVATE_KEY"),
		GHAppPersonalKey: req("GHAPP_PERSONAL_PRIVATE_KEY"),
	}

	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}

	if cfg.Prod {
		cfg.PostgresSchema = "prod"
		cfg.RunnerGroup = "RISE RISC-V Runners"
		cfg.RunnerPrefix = "rise-riscv-runner-"
		cfg.ImageUbuntu24 = fmt.Sprintf("%s/%s:ubuntu-24.04-prod", RunnerRegistry, RunnerImage)
		cfg.ImageUbuntu26 = fmt.Sprintf("%s/%s:ubuntu-26.04-prod", RunnerRegistry, RunnerImage)
	} else {
		cfg.PostgresSchema = "staging"
		cfg.RunnerGroup = "RISE RISC-V Runners (staging)"
		cfg.RunnerPrefix = "rise-riscv-runner-staging-"
		cfg.ImageUbuntu24 = fmt.Sprintf("%s/%s:ubuntu-24.04-staging", RunnerRegistry, RunnerImage)
		cfg.ImageUbuntu26 = fmt.Sprintf("%s/%s:ubuntu-26.04-staging", RunnerRegistry, RunnerImage)
	}

	return cfg, nil
}

// LoadConfigFromEnv is the production entry point; tests pass their own getenv.
func LoadConfigFromEnv() (Config, error) { return LoadConfig(os.Getenv) }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
