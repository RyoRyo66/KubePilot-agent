package providers

import (
	"context"
	"encoding/json"
	"kubeagent/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndependentReviewBindingAndEvidence(t *testing.T) {
	incident := core.Incident{Evidence: []core.Evidence{{ID: "known"}}, Diagnosis: &core.Diagnosis{Conclusion: "previous result"}}
	plan := core.Plan{Action: "restore_selector", BaselineID: "baseline"}
	decision := "approve"
	evidence := "known"
	hash := core.PlanHash(&plan)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		var messages []map[string]string
		_ = json.Unmarshal(body["messages"], &messages)
		if len(messages) != 2 {
			t.Error("review inherited tool conversation")
		}
		if strings.Contains(string(body["messages"]), "previous result") {
			t.Error("review inherited previous diagnosis")
		}
		if !strings.Contains(messages[1]["content"], "selector 复核资料") {
			t.Error("review did not receive the retrieved reference")
		}
		arguments, _ := json.Marshal(map[string]any{"decision": decision, "reason": "checked original evidence", "evidenceIds": []string{evidence}, "planHash": hash})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"tool_calls": []any{map[string]any{"type": "function", "id": "r1", "function": map[string]any{"name": "submit_review", "arguments": string(arguments)}}}}}}})
	}))
	defer srv.Close()
	m, e := NewCompatible(srv.URL, "fixture", "")
	if e != nil {
		t.Fatal(e)
	}
	m.Knowledge = core.NewKnowledgeIndex([]core.KnowledgeDocument{{ID: "doc-selector", Title: "selector 复核资料", Content: "对照就绪端点。"}})
	if review, err := m.Review(context.Background(), incident, plan); err != nil || len(review.Knowledge) != 1 || review.Knowledge[0].DocumentID != "doc-selector" {
		t.Fatal("missing checked review or its reference", err)
	}
	evidence = "doc-selector"
	if _, e = m.Review(context.Background(), incident, plan); e == nil {
		t.Fatal("forged evidence accepted")
	}
	evidence = "known"
	hash = "stale"
	if _, e = m.Review(context.Background(), incident, plan); e == nil {
		t.Fatal("stale plan accepted")
	}
}
