package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"kubeagent/internal/core"
	"kubeagent/internal/platform"
	"kubeagent/internal/providers"
)

// The scripted model cannot identify the quota until the real Connector reads the RS event.
func TestAdaptiveQuotaDiagnosis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools    []struct{ Function struct{ Name string } }
			Messages []struct{ Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Tools) != 1 || len(req.Messages) == 0 {
			t.Error("unexpected model request")
			w.WriteHeader(400)
			return
		}
		name := req.Tools[0].Function.Name
		input := req.Messages[len(req.Messages)-1].Content
		var material struct {
			Incident        struct{ Evidence []core.Evidence }
			Guide           []core.Passage
			NewEvidenceIDs  []string `json:"newEvidenceIds"`
			AvailableChecks []string
			PriorDecisions  []core.CheckDecision
		}
		if name != "connection_check" {
			if err := json.Unmarshal([]byte(input), &material); err != nil {
				t.Error(err)
			}
		}
		raw, _ := json.Marshal(material.Incident.Evidence)
		var args any
		switch name {
		case "connection_check":
			args = map[string]bool{"ok": true}
		case "triage_incident":
			if strings.Contains(string(raw), "exceeded quota") {
				t.Error("first pass should only see the rollout condition")
			}
			args = map[string]any{"disposition": "inspect", "conclusion": "发布超时，原因待核实", "evidenceIds": []string{"rollout-api-uid"}, "recommendations": []string{"核对发布条件与新副本集"}, "limitations": "首次巡检只有发布失败条件", "checks": []string{"rollout_status"}, "hypotheses": []core.Hypothesis{{Cause: "新副本可能未创建或未就绪", Check: "对照新副本集数量和就绪数", EvidenceIDs: []string{"rollout-api-uid"}}}}
		case "submit_diagnosis":
			if slices.Equal(material.NewEvidenceIDs, []string{"check-rollout_status-api-uid"}) {
				if !strings.Contains(string(raw), "ReplicaSet api-v2") || strings.Contains(string(raw), "team-memory") || slices.Contains(material.AvailableChecks, "rollout_status") || !slices.Contains(material.AvailableChecks, "target_events") {
					t.Error("wrong evidence or tool set after rollout read")
				}
				args = map[string]any{"disposition": "inspect", "conclusion": "新副本集已有记录但副本为零，需检查控制器事件", "evidenceIds": material.NewEvidenceIDs, "checks": []string{"target_events"}, "recommendations": []string{"查看创建被拒原因"}, "limitations": "发布条件不足以确定原因"}
				break
			}
			if len(material.PriorDecisions) != 1 || !slices.Equal(material.PriorDecisions[0].Checks, []string{"target_events"}) || !slices.Equal(material.NewEvidenceIDs, []string{"check-target_events-api-uid"}) {
				t.Error("missing intermediate decision or incorrect round evidence")
			}
			if !strings.Contains(string(raw), "exceeded quota: team-memory") {
				t.Error("second pass never received quota evidence")
			}
			if !slices.ContainsFunc(material.Guide, func(p core.Passage) bool { return p.Title == "team-memory 配额复盘" }) {
				t.Error("second pass did not retrieve workspace note")
			}
			args = map[string]any{"conclusion": "ReplicaSet 创建 Pod 被 team-memory 配额拒绝", "evidenceIds": []string{"check-target_events-api-uid"}, "recommendations": []string{"核对工作负载内存请求与命名空间剩余配额"}, "limitations": "实际剩余额度需管理员核实", "disposition": "consider_plan"}
		default:
			t.Errorf("unexpected model tool %s", name)
			w.WriteHeader(400)
			return
		}
		b, _ := json.Marshal(args)
		call := map[string]any{"id": "test", "type": "function", "function": map[string]string{"name": name, "arguments": string(b)}}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"tool_calls": []any{call}}}}})
	}))
	defer model.Close()
	s, err := platform.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	handler := s.Handler("", "")
	request := httptest.NewRequest("POST", "http://localhost/api/platform/knowledge", strings.NewReader(`{"title":"team-memory 配额复盘","content":"# 历史核查\nteam-memory 的 exceeded quota 需要对照资源请求与剩余配额。"}`))
	request.Header.Set("X-Requested-With", "KubePilot")
	request.Header.Set("Content-Type", "application/json")
	reply := httptest.NewRecorder()
	handler.ServeHTTP(reply, request)
	if reply.Code != 201 {
		t.Fatal(reply.Body)
	}
	if _, err = s.SaveSource("http://localhost:18081", true); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveModel(ctx, model.URL, "quota-fixture", "test-fixture"); err != nil {
		t.Fatal(err)
	}
	scope := platform.Scope{Namespace: "lab", Image: "connector:test", Automatic: true, Actions: []string{"restore_image"}, Targets: []platform.TargetSpec{{Kind: "Deployment", Name: "api", ProbeURL: "http://api.lab.svc/health"}}}
	if _, err = s.SaveScope(scope); err != nil {
		t.Fatal(err)
	}
	d := &apps.Deployment{ObjectMeta: meta.ObjectMeta{Namespace: "lab", Name: "api", UID: "api-uid", Generation: 2, ResourceVersion: "9"}, Status: apps.DeploymentStatus{ObservedGeneration: 2, Conditions: []apps.DeploymentCondition{{Type: apps.DeploymentProgressing, Status: v1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}}}
	rs := &apps.ReplicaSet{ObjectMeta: meta.ObjectMeta{Namespace: "lab", Name: "api-v2", UID: "rs-uid", OwnerReferences: []meta.OwnerReference{*meta.NewControllerRef(d, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})}}}
	e := &v1.Event{ObjectMeta: meta.ObjectMeta{Namespace: "lab", Name: "quota"}, InvolvedObject: v1.ObjectReference{UID: rs.UID, Kind: "ReplicaSet", Name: rs.Name}, Type: "Warning", Reason: "FailedCreate", Message: "exceeded quota: team-memory", LastTimestamp: meta.Now()}
	client := fake.NewSimpleClientset(d, rs, e)
	w := Worker{ID: "local-fixture", Config: platform.ConnectorConfig{Scope: scope, Generation: 1}, Provider: &providers.Kubernetes{Config: providers.KubeConfig{Namespace: "lab"}, Client: client}}
	poll := platform.Poll{Connection: platform.Connection{ID: w.ID, Namespace: "lab", Generation: 1, Provider: "kubernetes"}}
	if _, err = s.Poll(poll); err != nil {
		t.Fatal(err)
	}
	if err = s.Scan(); err != nil {
		t.Fatal(err)
	}
	var actions []string
	for s.View()["busy"] == true && ctx.Err() == nil {
		reply, err := s.Poll(poll)
		if err != nil {
			t.Fatal(err)
		}
		if j := reply.Job; j != nil {
			actions = append(actions, j.Action)
			if err = s.Result(w.Handle(ctx, *j)); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	cases := s.View()["cases"].([]platform.Case)
	if ctx.Err() != nil || len(cases) != 1 || !slices.Equal(actions, []string{"collect", "rollout_status", "target_events"}) {
		t.Fatalf("unexpected work: %v", actions)
	}
	if in := cases[0].Investigation; in == nil || len(in.FollowUps) != 2 || in.FollowUps[0].Disposition != "inspect" || in.FollowUps[1].Disposition != "consider_plan" {
		t.Fatal("missing round history")
	}
	i := cases[0].Incident
	if i.Diagnosis == nil || !slices.ContainsFunc(i.Diagnosis.Knowledge, func(p core.Passage) bool { return p.Title == "team-memory 配额复盘" }) {
		t.Fatal("missing persisted reference snapshot")
	}
	if i.Status != "manual" || i.ExecutionCount != 0 || i.Plan != nil || i.Diagnosis == nil || !strings.Contains(i.Diagnosis.Conclusion, "team-memory") {
		t.Fatalf("unexpected incident outcome: %+v", i)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "list" && action.GetVerb() != "get" {
			t.Fatal("unexpected cluster write", action.GetVerb())
		}
	}
}
