package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kubeagent/internal/core"
)

type compatibleTransport func(*http.Request) (*http.Response, error)

func (f compatibleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeepSeekThinkingToolCompatibility(t *testing.T) {
	incident := core.Incident{Type: "ServiceSelector", Target: core.Target{Kind: "Service", UID: "uid"}, Evidence: []core.Evidence{{ID: "e1"}}}
	plan := core.Plan{Action: "restore_selector", BaselineID: "baseline"}
	for _, base := range []string{"https://api.deepseek.com", "https://api.deepseek.com/v1", "https://compatible.example/v1"} {
		t.Run(base, func(t *testing.T) {
			model, _ := NewCompatible(base, "deepseek-flash", "private-test-key")
			calls := 0
			model.Client = &http.Client{Transport: compatibleTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				var definitions []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				}
				_ = json.Unmarshal(body["tools"], &definitions)
				tool := definitions[0].Function.Name
				if r.URL.Hostname() == "api.deepseek.com" {
					if string(body["tool_choice"]) != `"auto"` {
						t.Error("thinking mode would reject forced tool choice")
					}
				} else if !strings.Contains(string(body["tool_choice"]), tool) {
					t.Error("changed another provider's protocol")
				}
				if strings.Contains(string(body["thinking"]), "disabled") {
					t.Error("reasoning was disabled")
				}
				arguments := `{"ok":true}`
				if tool == "triage_incident" {
					arguments = `{"disposition":"inspect","checks":["service_backends"],"conclusion":"no endpoints","recommendations":["compare backends"],"limitations":"unconfirmed","evidenceIds":["e1"],"hypotheses":[{"cause":"selector drift","evidenceIds":["e1"],"check":"compare ready pods"}]}`
				}
				if tool == "submit_diagnosis" {
					arguments = `{"conclusion":"selector mismatch","evidenceIds":["e1"],"recommendations":["restore baseline selector"],"limitations":"lab evidence"}`
					if strings.Contains(string(body["messages"]), "firstObservation") {
						if !strings.Contains(string(body["messages"]), "compare ready pods") {
							t.Fatal("missing initial observations")
						}
						arguments = `{"conclusion":"selector mismatch","evidenceIds":["check-service_backends-uid"],"recommendations":["check baseline"],"limitations":"lab evidence","disposition":"consider_plan"}`
					}
				}
				if tool == "submit_review" {
					b, _ := json.Marshal(map[string]any{"decision": "approve", "reason": "baseline agrees", "evidenceIds": []string{"e1"}, "planHash": core.PlanHash(&plan)})
					arguments = string(b)
				}
				encoded, _ := json.Marshal(toolEnvelope(tool, arguments))
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded))}, nil
			})}
			if err := model.Probe(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Diagnose(context.Background(), incident); err != nil {
				t.Fatal(err)
			}
			first, err := model.Investigate(context.Background(), incident)
			if err != nil {
				t.Fatal(err)
			}
			observed := incident
			observed.Evidence = append(append([]core.Evidence(nil), incident.Evidence...), core.Evidence{ID: "check-service_backends-uid"})
			if d, err := model.AssessChecks(context.Background(), observed, first, []string{"check-service_backends-uid"}); err != nil || d.Disposition != "consider_plan" {
				t.Fatal("failed second observation", err)
			}
			if _, err := model.Review(context.Background(), incident, plan); err != nil {
				t.Fatal(err)
			}
			if calls != 5 {
				t.Fatal("missing probe, diagnosis or review")
			}
		})
	}
}

func TestModelErrorExplainsKnownCauseWithoutEchoingSecrets(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{`{"error":{"message":"tool_choice is unsupported in thinking mode; private-test-key secret-provider-response"}}`, "强制工具选择"},
		{`{"error":{"message":"Invalid model private-test-key","param":"model"}}`, "模型名称无效"},
		{`{"error":{"message":"secret-provider-response private-test-key"}}`, "拒绝请求参数"},
	} {
		err := modelHTTPError(&http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(test.body))})
		if !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "private-test-key") || strings.Contains(err.Error(), "secret-provider-response") {
			t.Fatalf("unsafe or unhelpful error: %v", err)
		}
	}
}

func toolEnvelope(tool, args string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"content": nil, "reasoning_content": "opaque private continuation", "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]string{"name": tool, "arguments": args}}}}}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 8}}
}
func TestCompatibleWireProtocolAndHistory(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-only-key" {
			t.Error("invalid endpoint or auth")
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(string(body["tools"]), "execute") || !strings.Contains(string(body["tools"]), "submit_diagnosis") {
			t.Error("invalid tools")
		}
		if calls == 2 && (!strings.Contains(string(body["messages"]), `"role":"tool"`) || !strings.Contains(string(body["messages"]), "opaque private continuation")) {
			t.Error("missing tool conversation")
		}
		_ = json.NewEncoder(w).Encode(toolEnvelope("read_evidence", `{"source":"all"}`))
	}))
	defer server.Close()
	m, err := NewCompatible(server.URL+"/v1", "test-model", "test-only-key")
	if err != nil {
		t.Fatal(err)
	}
	in := core.AgentInput{Request: core.AgentRequest{Namespace: "lab", Workload: "app"}, Incident: core.Incident{Type: "ImagePullBackOff"}}
	d, err := m.Next(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tool != "read_evidence" || d.InputTokens != 20 || d.OutputTokens != 8 {
		t.Fatal("invalid parsed call")
	}
	in.Exchanges = []core.ToolExchange{{Decision: d, Output: json.RawMessage("[]")}}
	if _, err = m.Next(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}
func TestCompatibleRejectsBadResponsesAndRedirect(t *testing.T) {
	for _, scenario := range []string{"401", "invalid-json", "truncated", "multiple-tools", "redirect", "oversized", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "401":
					w.WriteHeader(401)
					_, _ = w.Write([]byte("secret-provider-response"))
				case "invalid-json":
					_, _ = w.Write([]byte("invalid"))
				case "truncated":
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "length", "message": map[string]any{"tool_calls": []any{}}}}})
				case "multiple-tools":
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"tool_calls": []any{map[string]string{}, map[string]string{}}}}}})
				case "redirect":
					w.Header().Set("Location", "https://example.invalid/credential-sink")
					w.WriteHeader(302)
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024+10)))
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(500 * time.Millisecond):
					}
				}
			}))
			defer server.Close()
			m, _ := NewCompatible(server.URL, "test", "test-key")
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := m.Next(ctx, core.AgentInput{})
			if err == nil || strings.Contains(err.Error(), "secret-provider-response") || strings.Contains(err.Error(), "test-key") {
				t.Fatalf("unsafe error %v", err)
			}
		})
	}
}
func TestCompatibleConfigValidation(t *testing.T) {
	for _, base := range []string{"ftp://example.com", "https://user:pass@example.com", "http://remote.example.com", "https://example.com/?key=secret"} {
		if _, err := NewCompatible(base, "test", "test-key"); err == nil {
			t.Fatal("accepted", base)
		}
	}
	if _, err := NewCompatible("https://example.com/v1", "test", ""); err == nil {
		t.Fatal("missing remote key accepted")
	}
}

func TestCompatibleSingleDiagnosisValidatesEvidence(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "fabricated"}[valid], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				if !strings.Contains(string(body["tool_choice"]), "submit_diagnosis") {
					t.Error("diagnosis tool not selected")
				}
				if !strings.Contains(string(body["tools"]), `"enum":["e1"]`) {
					t.Error("diagnosis references are not constrained to supplied evidence")
				}
				reference := "e1"
				if !valid {
					reference = "invented"
				}
				args, _ := json.Marshal(map[string]any{"conclusion": "证据中的镜像异常", "evidenceIds": []string{reference}, "recommendations": []string{"核对基线"}, "limitations": "测试"})
				_ = json.NewEncoder(w).Encode(toolEnvelope("submit_diagnosis", string(args)))
			}))
			defer srv.Close()
			model, _ := NewCompatible(srv.URL, "test-model", "test-key")
			d, err := model.Diagnose(context.Background(), core.Incident{Type: "ImagePullBackOff", Evidence: []core.Evidence{{ID: "e1", Detail: "manifest unknown"}}})
			if valid && (err != nil || d.Engine != "compatible:test-model") {
				t.Fatal("valid diagnosis failed", err)
			}
			if !valid && err == nil {
				t.Fatal("fabricated evidence accepted")
			}
		})
	}
}
