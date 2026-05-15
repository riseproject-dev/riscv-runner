package internal

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testKeyPEM generates a fresh RSA private key as PEM. Used once per test
// run; the key is throwaway, doesn't touch any real GitHub.
func testKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

// newTestClient wires a GHClient to point at a httptest.Server. Callers
// register handlers on the returned mux.
func newTestClient(t *testing.T) (*GHClient, *http.ServeMux, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := Config{GHAppOrgKey: testKeyPEM(t), GHAppPersonalKey: testKeyPEM(t)}
	gh, err := NewGHClient(cfg)
	if err != nil {
		t.Fatalf("NewGHClient: %v", err)
	}
	gh.BaseURL = srv.URL
	gh.HTTP = srv.Client()
	return gh, mux, srv
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func TestNewGHClient_RejectsBadPEM(t *testing.T) {
	if _, err := NewGHClient(Config{GHAppOrgKey: "not pem", GHAppPersonalKey: testKeyPEM(t)}); err == nil {
		t.Error("expected error on bad org key")
	}
	if _, err := NewGHClient(Config{GHAppOrgKey: testKeyPEM(t), GHAppPersonalKey: "not pem"}); err == nil {
		t.Error("expected error on bad personal key")
	}
}

func TestGenerateJWT_SignsAndIncludesClaims(t *testing.T) {
	gh, _, _ := newTestClient(t)
	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	gh.Now = func() time.Time { return fixed }

	tok, err := gh.GenerateJWT(gh.OrgAppID)
	if err != nil {
		t.Fatalf("GenerateJWT: %v", err)
	}
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		return &gh.OrgKey.PublicKey, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("parse JWT: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if int64(claims["iat"].(float64)) != fixed.Unix() {
		t.Errorf("iat=%v want %v", claims["iat"], fixed.Unix())
	}
	if int64(claims["exp"].(float64)) != fixed.Add(10*time.Minute).Unix() {
		t.Errorf("exp wrong")
	}
	if int64(claims["iss"].(float64)) != gh.OrgAppID {
		t.Errorf("iss=%v want %d", claims["iss"], gh.OrgAppID)
	}
}

func TestGenerateJWT_UnknownAppID(t *testing.T) {
	gh, _, _ := newTestClient(t)
	if _, err := gh.GenerateJWT(99999); err == nil {
		t.Error("expected error on unknown app_id")
	}
}

func TestAuthenticateApp_SuccessAndCache(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	var calls int
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" {
			t.Errorf("method=%s", r.Method)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("missing Bearer auth: %q", got)
		}
		writeJSON(w, 201, map[string]string{"token": "ghs_xyz"})
	})

	tok, err := gh.AuthenticateApp(context.Background(), 42, gh.OrgAppID)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if tok != "ghs_xyz" {
		t.Errorf("token=%q", tok)
	}
	// Second call within TTL is served from cache.
	tok2, err := gh.AuthenticateApp(context.Background(), 42, gh.OrgAppID)
	if err != nil || tok2 != tok || calls != 1 {
		t.Errorf("expected cache hit; calls=%d tok2=%q", calls, tok2)
	}
}

func TestAuthenticateApp_ExpiryRefetches(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	gh.Now = func() time.Time { return now }
	gh.TokenTTL = 5 * time.Minute

	var calls int
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, 201, map[string]string{"token": fmt.Sprintf("t%d", calls)})
	})

	if _, err := gh.AuthenticateApp(context.Background(), 1, gh.OrgAppID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute) // TTL expired
	t2, err := gh.AuthenticateApp(context.Background(), 1, gh.OrgAppID)
	if err != nil {
		t.Fatal(err)
	}
	if t2 != "t2" || calls != 2 {
		t.Errorf("expected refetch; calls=%d tok=%s", calls, t2)
	}
}

func TestAuthenticateApp_404ReturnsAPIError(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/app/installations/99/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"message": "Not Found"})
	})
	_, err := gh.AuthenticateApp(context.Background(), 99, gh.OrgAppID)
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Fatalf("expected GitHubAPIError{404}, got %v", err)
	}
}

func TestAuthenticateApp_LRUEvictsOldest(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	gh.TokenMax = 2
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 201, map[string]string{"token": "t"})
	})
	for i := int64(1); i <= 3; i++ {
		if _, err := gh.AuthenticateApp(context.Background(), i, gh.OrgAppID); err != nil {
			t.Fatalf("auth %d: %v", i, err)
		}
	}
	if len(gh.tokens) != 2 {
		t.Errorf("expected 2 tokens after LRU, got %d", len(gh.tokens))
	}
}

func TestGetInstallation_OrgVsUserPicksApp(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/app/installations/10", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"id":      10,
			"account": map[string]any{"login": "acme", "type": "Organization", "id": 999},
		})
	})
	inst, err := gh.GetInstallation(context.Background(), 10, EntityOrganization)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if inst.Account.Login != "acme" || inst.Account.Type != "Organization" || inst.Account.ID != 999 {
		t.Errorf("account decoded wrong: %+v", inst.Account)
	}
	if len(inst.Raw) == 0 {
		t.Error("Raw not preserved")
	}
}

func TestGetInstallation_404(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/app/installations/0", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"message": "nope"})
	})
	_, err := gh.GetInstallation(context.Background(), 0, EntityUser)
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Errorf("expected 404 API err, got %v", err)
	}
}

func TestEnsureRunnerGroup_ReturnsExisting(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/orgs/acme/actions/runner-groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		writeJSON(w, 200, map[string]any{
			"runner_groups": []map[string]any{
				{"id": 7, "name": "other"},
				{"id": 13, "name": "RISE RISC-V Runners"},
			},
		})
	})
	id, err := gh.EnsureRunnerGroup(context.Background(), "tok", "acme", "RISE RISC-V Runners")
	if err != nil || id != 13 {
		t.Errorf("got id=%d err=%v want 13", id, err)
	}
}

func TestEnsureRunnerGroup_CreatesWhenMissing(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	var calls []string
	mux.HandleFunc("/orgs/acme/actions/runner-groups", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == "GET" {
			writeJSON(w, 200, map[string]any{"runner_groups": []map[string]any{{"id": 1, "name": "other"}}})
			return
		}
		// POST body has visibility:"all", allows_public_repositories:true
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["visibility"] != "all" || body["allows_public_repositories"] != true {
			t.Errorf("create body wrong: %+v", body)
		}
		writeJSON(w, 201, map[string]any{"id": 42})
	})
	id, err := gh.EnsureRunnerGroup(context.Background(), "tok", "acme", "RISE RISC-V Runners")
	if err != nil || id != 42 {
		t.Errorf("got id=%d err=%v want 42", id, err)
	}
	if len(calls) != 2 || calls[0] != "GET" || calls[1] != "POST" {
		t.Errorf("call order: %v", calls)
	}
}

func TestEnsureRunnerGroup_ListErrorPropagates(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/orgs/acme/actions/runner-groups", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 500, map[string]string{"message": "boom"})
	})
	_, err := gh.EnsureRunnerGroup(context.Background(), "tok", "acme", "g")
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 {
		t.Errorf("expected 500 API err, got %v", err)
	}
}

func TestEnsureRunnerGroup_CreateErrorPropagates(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/orgs/acme/actions/runner-groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			writeJSON(w, 200, map[string]any{"runner_groups": []any{}})
			return
		}
		writeJSON(w, 422, map[string]string{"message": "bad"})
	})
	_, err := gh.EnsureRunnerGroup(context.Background(), "tok", "acme", "g")
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 422 {
		t.Errorf("expected 422 API err, got %v", err)
	}
}

func TestCreateJITRunnerConfigOrg_PostsBody(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/orgs/acme/actions/runners/generate-jitconfig", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "r-1" || body["runner_group_id"].(float64) != 13 ||
			body["work_folder"] != "../../../work" {
			t.Errorf("body wrong: %+v", body)
		}
		writeJSON(w, 201, map[string]string{"encoded_jit_config": "ENC"})
	})
	got, err := gh.CreateJITRunnerConfigOrg(context.Background(), "tok", "acme", "r-1", 13, []string{"ubuntu-24.04-riscv"})
	if err != nil || got != "ENC" {
		t.Errorf("got %q err=%v", got, err)
	}
}

func TestCreateJITRunnerConfigRepo_UsesGroupID1(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/repos/user/proj/actions/runners/generate-jitconfig", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["runner_group_id"].(float64) != 1 {
			t.Errorf("repo JIT should use group_id=1, got %v", body["runner_group_id"])
		}
		writeJSON(w, 201, map[string]string{"encoded_jit_config": "ENC"})
	})
	got, err := gh.CreateJITRunnerConfigRepo(context.Background(), "tok", "user/proj", "r-1", []string{"x"})
	if err != nil || got != "ENC" {
		t.Errorf("got %q err=%v", got, err)
	}
}

func TestCreateJITRunnerConfig_NonCreatedIsError(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/orgs/acme/actions/runners/generate-jitconfig", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 422, map[string]string{"message": "bad"})
	})
	_, err := gh.CreateJITRunnerConfigOrg(context.Background(), "tok", "acme", "r", 1, nil)
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 422 {
		t.Errorf("expected 422 API err, got %v", err)
	}
}

func TestListRunnersOrgGroup_FollowsPagination(t *testing.T) {
	gh, mux, srv := newTestClient(t)
	base := "/orgs/acme/actions/runner-groups/7/runners"
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2&per_page=100>; rel="next"`, srv.URL, base))
			writeJSON(w, 200, map[string]any{"runners": []map[string]any{
				{"id": 1, "name": "a", "status": "online", "busy": false},
			}})
			return
		}
		writeJSON(w, 200, map[string]any{"runners": []map[string]any{
			{"id": 2, "name": "b", "status": "offline", "busy": false},
		}})
	})
	out, err := gh.ListRunnersOrgGroup(context.Background(), "tok", "acme", 7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("pagination didn't concatenate: %+v", out)
	}
}

func TestListRunnersRepo_NoPaginationHeader(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/repos/user/proj/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"runners": []map[string]any{
			{"id": 9, "name": "rise-riscv-runner-staging-abc", "status": "online", "busy": true},
		}})
	})
	out, err := gh.ListRunnersRepo(context.Background(), "tok", "user/proj")
	if err != nil || len(out) != 1 || !out[0].Busy {
		t.Errorf("got %+v err=%v", out, err)
	}
}

func TestListRunners_NonOKErrors(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/orgs/acme/actions/runner-groups/1/runners", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 500, map[string]string{"message": "boom"})
	})
	_, err := gh.ListRunnersOrgGroup(context.Background(), "tok", "acme", 1)
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 {
		t.Errorf("expected 500 API err, got %v", err)
	}
}

func TestDeleteRunner_204And404Succeed(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	for _, status := range []int{204, 404} {
		path := fmt.Sprintf("/orgs/acme/actions/runners/%d", status)
		mux.HandleFunc(path, func(s int) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(s) }
		}(status))
		if err := gh.DeleteRunnerOrg(context.Background(), "tok", "acme", int64(status)); err != nil {
			t.Errorf("status %d should be success: %v", status, err)
		}
	}
}

func TestDeleteRunnerOrg_5xxIsError(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/orgs/acme/actions/runners/1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	err := gh.DeleteRunnerOrg(context.Background(), "tok", "acme", 1)
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 {
		t.Errorf("expected 500 API err, got %v", err)
	}
}

func TestDeleteRunnerRepo_204(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/repos/user/proj/actions/runners/5", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})
	if err := gh.DeleteRunnerRepo(context.Background(), "tok", "user/proj", 5); err != nil {
		t.Errorf("expected success: %v", err)
	}
}

func TestGetJobInfo_Success(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/repos/user/proj/actions/jobs/77", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "in_progress", "conclusion": "cancelled", "runner_name": "rn"})
	})
	j, err := gh.GetJobInfo(context.Background(), "tok", "user/proj", 77)
	if err != nil {
		t.Fatalf("GetJobInfo: %v", err)
	}
	if j.Status != "in_progress" || j.Conclusion == nil || *j.Conclusion != "cancelled" || j.RunnerName != "rn" {
		t.Errorf("decoded wrong: %+v", j)
	}
}

func TestGetJobInfo_404(t *testing.T) {
	gh, mux, _ := newTestClient(t)
	mux.HandleFunc("/repos/user/proj/actions/jobs/0", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"message": "missing"})
	})
	_, err := gh.GetJobInfo(context.Background(), "tok", "user/proj", 0)
	var apiErr *GitHubAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Errorf("expected 404 API err, got %v", err)
	}
}

func TestNextLink(t *testing.T) {
	cases := []struct{ header, want string }{
		{"", ""},
		{`<https://api/x?page=2>; rel="next", <https://api/x?page=5>; rel="last"`, "https://api/x?page=2"},
		{`<https://api/x?page=1>; rel="first"`, ""},
	}
	for i, c := range cases {
		if got := nextLink(c.header); got != c.want {
			t.Errorf("case %d: nextLink(%q)=%q want %q", i, c.header, got, c.want)
		}
	}
}

func TestApiMessage_PrefersStructured(t *testing.T) {
	got := apiMessage([]byte(`{"message":"hi"}`))
	if got != "hi" {
		t.Errorf("got %q want hi", got)
	}
	got = apiMessage([]byte("plain"))
	if got != "plain" {
		t.Errorf("got %q want fall-through", got)
	}
}

func TestDoJSON_TransportError(t *testing.T) {
	gh := &GHClient{
		BaseURL: "http://127.0.0.1:1", // unreachable
		HTTP:    &http.Client{Timeout: 100 * time.Millisecond},
	}
	_, _, err := gh.doJSON(context.Background(), "GET", "/x", nil, "tok")
	if err == nil {
		t.Error("expected transport error")
	}
}

// silence unused-import lint when imports rotate
var _ = io.Discard
