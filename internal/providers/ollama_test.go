package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kubeagent/internal/core"
)

func TestModelOutputRequiresExistingEvidence(t *testing.T) {
	for _, scenario := range []string{"valid", "invented-reference", "invalid-json", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req["stream"] != false || req["format"] != "json" {
					t.Error("unexpected model request")
				}
				content := `{"conclusion":"镜像拉取失败，需核对仓库与标签。","evidenceIds":["ev-1"],"recommendations":["核对标签"],"limitations":"证据有限"}`
				if scenario == "invented-reference" {
					content = `{"conclusion":"故障","evidenceIds":["made-up"]}`
				}
				if scenario == "invalid-json" {
					content = "not json"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}, "done": scenario != "incomplete"})
			}))
			defer s.Close()
			model := Ollama{URL: s.URL, Model: "test-model"}
			_, err := model.Diagnose(context.Background(), core.Incident{Type: "ImagePullBackOff", Evidence: []core.Evidence{{ID: "ev-1", Detail: "pull failed"}}})
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("invalid validation result: %v", err)
			}
		})
	}
}
