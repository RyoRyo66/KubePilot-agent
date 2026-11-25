package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"kubeagent/internal/core"
	"kubeagent/internal/providers"
)

func testAPI(t *testing.T, token string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	demo, err := providers.NewDemo(filepath.Join(dir, "demo.json"), "lab")
	if err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(core.FileStore{Path: filepath.Join(dir, "state.json")}, demo, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return API{Engine: e, Token: token}.Handler()
}
func TestAPIWriteGuards(t *testing.T) {
	handler := testAPI(t, "")
	for _, scenario := range []struct {
		name, host, origin, marker, body string
		want                             int
	}{
		{"normal", "127.0.0.1:18080", "http://127.0.0.1:18080", "KubePilot", `{}`, 200},
		{"cross-origin", "127.0.0.1:18080", "https://untrusted.example", "KubePilot", `{}`, 403},
		{"missing-marker", "127.0.0.1:18080", "", "", `{}`, 403},
		{"dns-rebinding", "untrusted.example", "http://untrusted.example", "KubePilot", `{}`, 403},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18080/api/inspections", strings.NewReader(scenario.body))
			req.Host = scenario.host
			req.Header.Set("Origin", scenario.origin)
			req.Header.Set("X-Requested-With", scenario.marker)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != scenario.want {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
		})
	}
}
func TestTokenAndInvalidPayload(t *testing.T) {
	handler := testAPI(t, "test-only-token")
	for _, auth := range []string{"", "Bearer wrong", "test-only-token", "Bearer test-only-token"} {
		req := httptest.NewRequest("GET", "http://127.0.0.1:18080/api/state", nil)
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		want := 401
		if auth == "Bearer test-only-token" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("token guard: %d", w.Code)
		}
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:18080/api/demo/inject", strings.NewReader(`{"fault":"Pending","command":"delete all"}`))
	req.Header.Set("Authorization", "Bearer test-only-token")
	req.Header.Set("X-Requested-With", "KubePilot")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("unknown fields accepted: %d", w.Code)
	}
}
