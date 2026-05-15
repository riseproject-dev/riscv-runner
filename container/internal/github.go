package internal

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// HTTPDoer is the minimal interface ghClient needs from *http.Client.
// Tests pass an httptest.Server-backed *http.Client; production uses http.DefaultClient.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// GHClient implements GitHubClient over the live GitHub REST API.
// Installation tokens are issued by GitHub with a 1-hour lifetime; we cache
// them for 59 minutes (= 1 hour minus clock skew safety) keyed by
// (installation_id, app_id).
type GHClient struct {
	HTTP      HTTPDoer
	BaseURL   string // defaults to https://api.github.com; tests override
	OrgAppID  int64
	PersAppID int64
	OrgKey    *rsa.PrivateKey
	PersKey   *rsa.PrivateKey
	TokenTTL  time.Duration
	TokenMax  int
	Now       func() time.Time

	mu     sync.Mutex
	tokens map[ghTokenKey]ghToken
	order  []ghTokenKey
}

type ghTokenKey struct {
	InstallationID int64
	AppID          int64
}

type ghToken struct {
	value     string
	expiresAt time.Time
}

// NewGHClient wires a GHClient from a Config.
func NewGHClient(cfg Config) (*GHClient, error) {
	orgKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(cfg.GHAppOrgKey))
	if err != nil {
		return nil, fmt.Errorf("parse GHAPP_ORG_PRIVATE_KEY: %w", err)
	}
	persKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(cfg.GHAppPersonalKey))
	if err != nil {
		return nil, fmt.Errorf("parse GHAPP_PERSONAL_PRIVATE_KEY: %w", err)
	}
	return &GHClient{
		HTTP:      http.DefaultClient,
		BaseURL:   "https://api.github.com",
		OrgAppID:  GHAppOrgID,
		PersAppID: GHAppPersonalID,
		OrgKey:    orgKey,
		PersKey:   persKey,
		TokenTTL:  59 * time.Minute,
		TokenMax:  1024,
		Now:       time.Now,
		tokens:    map[ghTokenKey]ghToken{},
	}, nil
}

func (c *GHClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// GenerateJWT signs the app JWT with the matching private key.
// Exported so tests can verify produced tokens.
func (c *GHClient) GenerateJWT(appID int64) (string, error) {
	var key *rsa.PrivateKey
	switch appID {
	case c.OrgAppID:
		key = c.OrgKey
	case c.PersAppID:
		key = c.PersKey
	default:
		return "", fmt.Errorf("unknown app_id: %d", appID)
	}
	now := c.now()
	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": appID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

func (c *GHClient) AuthenticateApp(ctx context.Context, installationID, appID int64) (string, error) {
	key := ghTokenKey{installationID, appID}
	c.mu.Lock()
	if t, ok := c.tokens[key]; ok && c.now().Before(t.expiresAt) {
		c.mu.Unlock()
		return t.value, nil
	}
	c.mu.Unlock()

	jwtTok, err := c.GenerateJWT(appID)
	if err != nil {
		return "", err
	}
	body, status, err := c.doJSON(ctx, "POST", "/app/installations/"+i64(installationID)+"/access_tokens", nil, jwtTok)
	if err != nil {
		return "", err
	}
	if status != 201 {
		return "", &GitHubAPIError{StatusCode: status, Message: apiMessage(body)}
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}

	c.mu.Lock()
	c.tokens[key] = ghToken{value: resp.Token, expiresAt: c.now().Add(c.TokenTTL)}
	c.order = append(c.order, key)
	for len(c.order) > c.TokenMax {
		delete(c.tokens, c.order[0])
		c.order = c.order[1:]
	}
	c.mu.Unlock()
	return resp.Token, nil
}

func (c *GHClient) GetInstallation(ctx context.Context, installationID int64, et EntityType) (Installation, error) {
	appID := c.OrgAppID
	if et == EntityUser {
		appID = c.PersAppID
	}
	jwtTok, err := c.GenerateJWT(appID)
	if err != nil {
		return Installation{}, err
	}
	body, status, err := c.doJSON(ctx, "GET", "/app/installations/"+i64(installationID), nil, jwtTok)
	if err != nil {
		return Installation{}, err
	}
	if status != 200 {
		return Installation{}, &GitHubAPIError{StatusCode: status, Message: apiMessage(body)}
	}
	var inst Installation
	if err := json.Unmarshal(body, &inst); err != nil {
		return Installation{}, err
	}
	inst.Raw = body
	return inst, nil
}

// EnsureRunnerGroup returns the existing group id or creates the group.
// Callers log success with their full Entity context.
func (c *GHClient) EnsureRunnerGroup(ctx context.Context, token, orgName, groupName string) (int64, error) {
	listURL := "/orgs/" + orgName + "/actions/runner-groups"
	body, status, err := c.doJSON(ctx, "GET", listURL, nil, token)
	if err != nil {
		return 0, err
	}
	if status != 200 {
		return 0, &GitHubAPIError{StatusCode: status, Message: apiMessage(body)}
	}
	var lst struct {
		RunnerGroups []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"runner_groups"`
	}
	if err := json.Unmarshal(body, &lst); err != nil {
		return 0, err
	}
	for _, g := range lst.RunnerGroups {
		if g.Name == groupName {
			return g.ID, nil
		}
	}
	createBody := map[string]any{
		"name":                       groupName,
		"visibility":                 "all",
		"allows_public_repositories": true,
	}
	body, status, err = c.doJSON(ctx, "POST", listURL, createBody, token)
	if err != nil {
		return 0, err
	}
	if status != 201 {
		return 0, &GitHubAPIError{StatusCode: status, Message: apiMessage(body)}
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

func (c *GHClient) CreateJITRunnerConfigOrg(ctx context.Context, token, orgName, runnerName string, groupID int64, labels []string) (string, error) {
	body := map[string]any{
		"name":            runnerName,
		"runner_group_id": groupID,
		"labels":          labels,
		"work_folder":     "../../../work",
	}
	return c.jitconfig(ctx, "/orgs/"+orgName+"/actions/runners/generate-jitconfig", body, token)
}

func (c *GHClient) CreateJITRunnerConfigRepo(ctx context.Context, token, repoFullName, runnerName string, labels []string) (string, error) {
	body := map[string]any{
		"name":            runnerName,
		"runner_group_id": 1,
		"labels":          labels,
		"work_folder":     "../../../work",
	}
	return c.jitconfig(ctx, "/repos/"+repoFullName+"/actions/runners/generate-jitconfig", body, token)
}

func (c *GHClient) jitconfig(ctx context.Context, path string, body map[string]any, token string) (string, error) {
	respBody, status, err := c.doJSON(ctx, "POST", path, body, token)
	if err != nil {
		return "", err
	}
	if status != 201 {
		return "", &GitHubAPIError{StatusCode: status, Message: apiMessage(respBody)}
	}
	var resp struct {
		EncodedJITConfig string `json:"encoded_jit_config"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", err
	}
	return resp.EncodedJITConfig, nil
}

func (c *GHClient) ListRunnersOrgGroup(ctx context.Context, token, orgName string, groupID int64) ([]GHRunner, error) {
	return c.paginatedRunners(ctx, "/orgs/"+orgName+"/actions/runner-groups/"+i64(groupID)+"/runners", token)
}

func (c *GHClient) ListRunnersRepo(ctx context.Context, token, repoFullName string) ([]GHRunner, error) {
	return c.paginatedRunners(ctx, "/repos/"+repoFullName+"/actions/runners", token)
}

func (c *GHClient) paginatedRunners(ctx context.Context, path, token string) ([]GHRunner, error) {
	var all []GHRunner
	next := c.BaseURL + path + "?per_page=100"
	for next != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", next, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, &GitHubAPIError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("GET %s: %s", next, body)}
		}
		var page struct {
			Runners []GHRunner `json:"runners"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Runners...)
		next = nextLink(resp.Header.Get("Link"))
	}
	return all, nil
}

// nextLink returns the URL for rel="next" in a GitHub Link header, or "".
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		if !strings.HasSuffix(p, `rel="next"`) {
			continue
		}
		lt := strings.Index(p, "<")
		gt := strings.Index(p, ">")
		if lt >= 0 && gt > lt {
			return p[lt+1 : gt]
		}
	}
	return ""
}

func (c *GHClient) DeleteRunnerOrg(ctx context.Context, token, orgName string, runnerID int64) error {
	return c.deleteRunner(ctx, "/orgs/"+orgName+"/actions/runners/"+i64(runnerID), token)
}

func (c *GHClient) DeleteRunnerRepo(ctx context.Context, token, repoFullName string, runnerID int64) error {
	return c.deleteRunner(ctx, "/repos/"+repoFullName+"/actions/runners/"+i64(runnerID), token)
}

func (c *GHClient) deleteRunner(ctx context.Context, path, token string) error {
	body, status, err := c.doJSON(ctx, "DELETE", path, nil, token)
	if err != nil {
		return err
	}
	if status == 204 || status == 404 {
		return nil
	}
	return &GitHubAPIError{StatusCode: status, Message: fmt.Sprintf("DELETE %s: %s", path, body)}
}

func (c *GHClient) GetJobInfo(ctx context.Context, token, repoFullName string, jobID int64) (GHJob, error) {
	body, status, err := c.doJSON(ctx, "GET", "/repos/"+repoFullName+"/actions/jobs/"+i64(jobID), nil, token)
	if err != nil {
		return GHJob{}, err
	}
	if status != 200 {
		return GHJob{}, &GitHubAPIError{StatusCode: status, Message: fmt.Sprintf("get job %d: %s", jobID, body)}
	}
	var j GHJob
	if err := json.Unmarshal(body, &j); err != nil {
		return GHJob{}, err
	}
	return j, nil
}

func (c *GHClient) GetRunInfo(ctx context.Context, token, repoFullName string, runID int64) (GHRun, error) {
	body, status, err := c.doJSON(ctx, "GET", "/repos/"+repoFullName+"/actions/runs/"+i64(runID), nil, token)
	if err != nil {
		return GHRun{}, err
	}
	if status != 200 {
		return GHRun{}, &GitHubAPIError{StatusCode: status, Message: fmt.Sprintf("get run %d: %s", runID, body)}
	}
	var r GHRun
	if err := json.Unmarshal(body, &r); err != nil {
		return GHRun{}, err
	}
	return r, nil
}

// doJSON sends a JSON-encoded request and returns body, status, err.
// `auth` is the value put after "Bearer " in the Authorization header.
func (c *GHClient) doJSON(ctx context.Context, method, path string, body any, auth string) ([]byte, int, error) {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		buf = bytes.NewReader(b)
	}
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, buf)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if buf != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	return rb, resp.StatusCode, err
}

// apiMessage best-effort extracts {"message":...} from a GitHub error body.
func apiMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		return m.Message
	}
	return string(body)
}

func i64(n int64) string { return fmt.Sprintf("%d", n) }

var _ = errors.New // reserved
