package server

import (
	"context"
	"encoding/json"
	"kubeagent/internal/core"
	"kubeagent/internal/providers"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentHTTPWorkflowAndRequestGuards(t *testing.T) {
	dir := t.TempDir()
	demo, _ := providers.NewDemo(filepath.Join(dir, "demo.json"), "lab")
	e, err := core.NewEngine(core.FileStore{Path: filepath.Join(dir, "state.json")}, demo, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a := core.NewAgentRunner(context.Background(), e, nil, 0)
	defer a.Close()
	handler := API{Engine: e, Agent: a, Token: "api-test-token"}.Handler()
	call := func(method, path, body, token, marker string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", marker)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	body := `{"namespace":"lab","workload":"checkout-api","mode":"approval","goal":"测试目标"}`
	if w := call("POST", "/api/agent/runs", body, "wrong", "KubePilot"); w.Code != 401 {
		t.Fatal("token bypass", w.Code)
	}
	if w := call("POST", "/api/agent/runs", body, "api-test-token", ""); w.Code != 403 {
		t.Fatal("marker bypass", w.Code)
	}
	if w := call("POST", "/api/agent/runs", `{"namespace":"lab","workload":"checkout-api","mode":"approval","shell":"rm"}`, "api-test-token", "KubePilot"); w.Code != 400 {
		t.Fatal("unknown field accepted", w.Code)
	}
	w := call("POST", "/api/agent/runs", body, "api-test-token", "KubePilot")
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var run core.AgentRun
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	wait := func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			w = call("GET", "/api/agent/runs/"+run.ID, "", "api-test-token", "")
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			_ = json.Unmarshal(w.Body.Bytes(), &run)
			if run.Terminal() || run.Status == "awaiting_approval" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("run timed out")
	}
	wait()
	if run.Status != "awaiting_approval" {
		t.Fatal(run.Status, run.Summary)
	}
	if w := call("POST", "/api/agent/runs/"+run.ID+"/approve", `{"planHash":"wrong"}`, "api-test-token", "KubePilot"); w.Code != 409 {
		t.Fatal("plan mismatch accepted")
	}
	approval, _ := json.Marshal(map[string]string{"planHash": run.PlanHash})
	w = call("POST", "/api/agent/runs/"+run.ID+"/approve", string(approval), "api-test-token", "KubePilot")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	wait()
	if run.Status != "recovered" {
		t.Fatal(run.Status, run.Summary)
	}
	approved := false
	for _, entry := range e.State().Audits {
		if entry.Action == "agent.approve" {
			approved = true
		}
	}
	if !approved {
		t.Fatal("missing approval audit")
	}
	if w := call("GET", "/api/agent/runs/missing", "", "api-test-token", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
