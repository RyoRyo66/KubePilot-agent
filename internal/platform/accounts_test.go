package platform

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const accountTestPassword = "test-password-only-2026"

type accountTestClient struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
}

func (c *accountTestClient) call(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://localhost:18081"+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "KubePilot")
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	for _, cookie := range w.Result().Cookies() {
		c.cookie = cookie
	}
	return w
}
func (c *accountTestClient) register(username string) {
	c.t.Helper()
	w := c.call("POST", "/api/auth/register", map[string]string{"username": username, "password": accountTestPassword, "confirmation": accountTestPassword})
	if w.Code != 200 {
		c.t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	if c.cookie == nil || !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteStrictMode {
		c.t.Fatal("session cookie protections missing")
	}
}
func (c *accountTestClient) create() string {
	c.t.Helper()
	w := c.call("POST", "/api/workspace/create", map[string]string{"name": "实验工作空间", "description": "个人实验"})
	if w.Code != 200 {
		c.t.Fatalf("create: %s", w.Body)
	}
	var body struct {
		Workspace Workspace `json:"workspace"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Workspace.ID
}
func TestAccountsIsolationAndOwnership(t *testing.T) {
	dir := t.TempDir()
	h, err := NewHub(dir, HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	a := &accountTestClient{t: t, handler: h.Handler("")}
	b := &accountTestClient{t: t, handler: h.Handler("")}
	if a.call("GET", "/api/platform/state", nil).Code != 401 {
		t.Fatal("anonymous access")
	}
	a.register("示例用户")
	if a.call("GET", "/api/platform/state", nil).Code != 409 {
		t.Fatal("workspace required")
	}
	aid := a.create()
	w := a.call("POST", "/api/platform/setup/source", map[string]any{"publicUrl": "http://localhost:18081", "allowHttp": true})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	b.register("second-user")
	bid := b.create()
	if aid == bid {
		t.Fatal("shared workspace ID")
	}
	if b.create() != bid {
		t.Fatal("duplicate workspace creation")
	}
	// A stale browser tab must not silently write to the newly logged-in account.
	stale := httptest.NewRequest("POST", "http://localhost:18081/api/workspace/update", strings.NewReader(`{"name":"stale tab"}`))
	stale.AddCookie(b.cookie)
	stale.Header.Set("Content-Type", "application/json")
	stale.Header.Set("X-Requested-With", "KubePilot")
	stale.Header.Set("X-KubePilot-Account", h.state.Workspaces[aid].OwnerID)
	stale.Header.Set("X-KubePilot-Workspace", aid)
	blocked := httptest.NewRecorder()
	h.Handler("").ServeHTTP(blocked, stale)
	if blocked.Code != 409 || h.state.Workspaces[bid].Name != "实验工作空间" {
		t.Fatal("stale account request was accepted")
	}
	w = b.call("GET", "/api/platform/state?workspaceId="+aid, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "localhost:18081") {
		t.Fatal("cross-account configuration read")
	}
	w = b.call("POST", "/api/workspace/update", map[string]string{"name": "hijack", "ownerId": h.state.Workspaces[aid].OwnerID})
	if w.Code == 200 {
		t.Fatal("owner ID accepted")
	}
	if h.state.Workspaces[aid].Name != "实验工作空间" {
		t.Fatal("other workspace changed")
	}
	if b.call("POST", "/api/platform/cases/foreign-incident/approve", map[string]string{"planHash": "anything"}).Code == 202 {
		t.Fatal("foreign action accepted")
	}
	if a.call("POST", "/api/workspace/complete", map[string]string{}).Code == 200 {
		t.Fatal("unconfigured onboarding marked complete")
	}
	// Cookie authentication cannot be bypassed by a webhook or the old admin bearer.
	r := httptest.NewRequest("GET", "http://localhost:18081/api/platform/state", nil)
	r.Header.Set("Authorization", "Bearer "+h.services[aid].current().Config.WebhookToken)
	out := httptest.NewRecorder()
	h.Handler("").ServeHTTP(out, r)
	if out.Code != 401 {
		t.Fatal("token confused with login")
	}
	// Webhook routes select a workspace using a separate high-entropy credential.
	payload := `{"version":"4","alerts":[{"status":"firing","labels":{"alertname":"OwnedAlert"},"startsAt":"2026-11-12T00:00:00Z"}]}`
	r = httptest.NewRequest("POST", "http://localhost:18081/ingest/alertmanager", strings.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+h.services[aid].current().Config.WebhookToken)
	out = httptest.NewRecorder()
	h.Handler("").ServeHTTP(out, r)
	if out.Code != 202 {
		t.Fatal(out.Body)
	}
	if len(h.services[aid].current().Alerts) != 1 || len(h.services[bid].current().Alerts) != 0 {
		t.Fatal("webhook routed to wrong workspace")
	}
	w = a.call("GET", "/api/auth/session", nil)
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "salt") {
		t.Fatal("credential hash exposed")
	}
	enc, err := os.ReadFile(filepath.Join(dir, "identity", "platform.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte(accountTestPassword)) || bytes.Contains(enc, []byte("示例用户")) {
		t.Fatal("plaintext identity file")
	}
}
func TestSessionPersistenceLogoutResetAndLock(t *testing.T) {
	dir := t.TempDir()
	h, err := NewHub(dir, HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := &accountTestClient{t: t, handler: h.Handler("")}
	a.register("alice")
	id := a.create()
	cookie := *a.cookie
	if other, e := NewHub(dir, HubOptions{}); e == nil {
		other.Close()
		t.Fatal("concurrent state writer accepted")
	}
	h.Close()
	h, err = NewHub(dir, HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	a.handler = h.Handler("")
	if a.call("GET", "/api/platform/state", nil).Code != 200 || h.state.Workspaces[id].Name != "实验工作空间" {
		t.Fatal("session or workspace not persisted")
	}
	if a.call("POST", "/api/auth/logout", map[string]string{}).Code != 200 {
		t.Fatal("logout failed")
	}
	a.cookie = &cookie
	if a.call("GET", "/api/platform/state", nil).Code != 401 {
		t.Fatal("logged out cookie accepted")
	}
	a.call("POST", "/api/auth/login", map[string]string{"username": "alice", "password": accountTestPassword})
	oldCookie := *a.cookie
	password := "replacement-password-2026"
	w := a.call("POST", "/api/auth/password", map[string]string{"currentPassword": accountTestPassword, "password": password, "confirmation": password})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	a.cookie = &oldCookie
	if a.call("GET", "/api/platform/state", nil).Code != 401 {
		t.Fatal("old session survived password change")
	}
	if a.call("POST", "/api/auth/login", map[string]string{"username": "alice", "password": accountTestPassword}).Code != 401 {
		t.Fatal("old password accepted")
	}
	if a.call("POST", "/api/auth/login", map[string]string{"username": "ALICE", "password": password}).Code != 200 {
		t.Fatal("normalized login failed")
	}
	if err = h.ResetPassword("alice", accountTestPassword); err != nil {
		t.Fatal(err)
	}
	if a.call("GET", "/api/platform/state", nil).Code != 401 {
		t.Fatal("admin reset did not revoke sessions")
	}
	if h.state.Workspaces[id].ID != id {
		t.Fatal("reset removed workspace")
	}
}
func TestAuthValidationCSRFExpiryAndThrottling(t *testing.T) {
	h, err := NewHub(t.TempDir(), HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	a := &accountTestClient{t: t, handler: h.Handler("")}
	for _, path := range []string{"/api/auth/register", "/api/auth/login", "/api/workspace/create"} {
		r := httptest.NewRequest("POST", "http://localhost:18081"+path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://other.example")
		r.Header.Set("X-Requested-With", "KubePilot")
		w := httptest.NewRecorder()
		a.handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("cross origin accepted")
		}
		r.Header.Del("Origin")
		r.Header.Del("X-Requested-With")
		w = httptest.NewRecorder()
		a.handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("CSRF header missing")
		}
	}
	w := a.call("POST", "/api/auth/register", map[string]string{"username": "alice", "password": "short", "confirmation": "short"})
	if w.Code == 200 {
		t.Fatal("weak password accepted")
	}
	a.register("alice")
	if a.call("POST", "/api/auth/register", map[string]string{"username": "ALICE", "password": accountTestPassword, "confirmation": accountTestPassword}).Code == 200 {
		t.Fatal("case duplicate registered")
	}
	token := a.cookie.Value
	h.mu.Lock()
	s := h.state.Sessions[sessionHash(token)]
	s.ExpiresAt = time.Now().Add(-time.Second)
	h.state.Sessions[sessionHash(token)] = s
	h.mu.Unlock()
	if a.call("POST", "/api/workspace/create", map[string]string{"name": "expired"}).Code != 401 {
		t.Fatal("expired session accepted")
	}
	wrong := a.call("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "wrong"})
	unknown := a.call("POST", "/api/auth/login", map[string]string{"username": "unknown", "password": "wrong"})
	if wrong.Body.String() != unknown.Body.String() {
		t.Fatal("login disclosed account existence")
	}
	for i := 0; i < 10; i++ {
		w = a.call("POST", "/api/auth/login", map[string]string{"username": "unknown", "password": "wrong"})
	}
	if w.Code != 429 {
		t.Fatal("login rate limit missing")
	}
	secureHandler := h.Handler("")
	h.options.PublicOrigin = "https://agent.example"
	r := httptest.NewRequest("GET", "https://agent.example/api/auth/session", nil)
	out := httptest.NewRecorder()
	h.setCookie(out, r, "test-token")
	c := out.Result().Cookies()[0]
	if !c.Secure || c.Name != "__Host-kubepilot_session" || c.Domain != "" {
		t.Fatal("HTTPS cookie protections missing")
	}
	r = httptest.NewRequest("GET", "https://wrong.example/api/auth/session", nil)
	out = httptest.NewRecorder()
	secureHandler.ServeHTTP(out, r)
	if out.Code != 403 {
		t.Fatal("host guard missing")
	}
}
func TestWorkspaceCreationConcurrentAndLegacyExplicit(t *testing.T) {
	dir := t.TempDir()
	legacy, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = legacy.SaveSource("http://localhost:18081", true)
	legacy.Close()
	original, _ := os.ReadFile(filepath.Join(dir, "platform.enc"))
	h, err := NewHub(dir, HubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	token, err := h.register("alice", accountTestPassword, accountTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := h.identity(token)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws, e := h.createWorkspace(a.ID, "same name", "")
			if e != nil {
				t.Error(e)
				return
			}
			ids <- ws.ID
		}()
	}
	wg.Wait()
	close(ids)
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatal("duplicate workspaces")
	}
	unchanged, _ := os.ReadFile(filepath.Join(dir, "platform.enc"))
	if !bytes.Equal(original, unchanged) {
		t.Fatal("unclaimed legacy state changed")
	}
	_, ws, _ := h.identity(token)
	if h.services[ws.ID].current().Config.PublicURL != "" {
		t.Fatal("arbitrary first user claimed legacy")
	}
	if err = h.ClaimLegacy("alice"); err != nil {
		t.Fatal(err)
	}
	if !h.state.Workspaces[ws.ID].Legacy || h.services[ws.ID].current().Config.PublicURL == "" {
		t.Fatal("explicit migration missing")
	}
	if h.services[ws.ID].current().Config.Scope.Automatic {
		t.Fatal("migration enabled automatic repair")
	}
	if h.ClaimLegacy("alice") == nil {
		t.Fatal("duplicate claim accepted")
	}
	if err = h.store.save(h.state); err != nil {
		t.Fatal(err)
	}
}

func TestClosedRegistrationAndMethodHandling(t *testing.T) {
	h, err := NewHub(t.TempDir(), HubOptions{RegistrationClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	a := &accountTestClient{t: t, handler: h.Handler("")}
	if a.call("POST", "/api/auth/register", map[string]string{}).Code != 403 {
		t.Fatal("closed registration accepted")
	}
	for _, path := range []string{"/api/auth/login", "/api/auth/register", "/api/auth/missing"} {
		if a.call("GET", path, nil).Code == 200 {
			t.Fatal("invalid public method")
		}
	}
	if a.call("GET", "/api/auth/session", nil).Code != 200 {
		t.Fatal("anonymous session unavailable")
	}
	// The HTTP server and UI never need an old administrator bearer token.
	r := httptest.NewRequest("GET", "http://localhost:18081/api/platform/state", nil)
	r.Header.Set("Authorization", "Bearer old-admin-token")
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	if w.Code != 401 || len(b) == 0 {
		t.Fatal("old bearer bypass")
	}
}
