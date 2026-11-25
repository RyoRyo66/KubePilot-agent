package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kubeagent/internal/core"
)

type testAgentModel struct{ mode string }

func (m testAgentModel) Name() string { return "test:model" }
func (m testAgentModel) Next(ctx context.Context, in core.AgentInput) (core.AgentDecision, error) {
	if m.mode == "timeout" {
		<-ctx.Done()
		return core.AgentDecision{}, ctx.Err()
	}
	if m.mode == "unavailable" {
		return core.AgentDecision{}, errors.New("model unavailable")
	}
	name, args := "read_evidence", any(map[string]string{"source": "all"})
	if m.mode == "unauthorized" {
		name, args = "delete_namespace", map[string]string{"namespace": "kube-system"}
	} else if len(in.Exchanges) == 1 {
		name, args = "search_runbooks", map[string]string{"query": in.Incident.Type}
	} else if len(in.Exchanges) >= 2 {
		reference := in.Incident.Evidence[0].ID
		if m.mode == "fabrication" {
			reference = "invented-evidence"
		}
		name, args = "submit_diagnosis", map[string]any{"conclusion": "模型根据已读取证据分析镜像异常", "evidenceIds": []string{reference}, "recommendations": []string{"核对健康基线"}, "limitations": "需要执行器独立验证"}
	}
	if m.mode == "loop" {
		name, args = "read_evidence", map[string]string{"source": "all"}
	}
	body, _ := json.Marshal(args)
	return core.AgentDecision{ID: time.Now().Format("150405.000000000"), Tool: name, Arguments: body, InputTokens: 12, OutputTokens: 7, Reasoning: "private model reasoning"}, nil
}
func agentSetup(t *testing.T, model core.AgentModel, timeout time.Duration) (*core.Engine, *core.AgentRunner, *controlledProvider, core.FileStore) {
	t.Helper()
	p := &controlledProvider{healthy: true}
	store := core.FileStore{Path: filepath.Join(t.TempDir(), "state.json")}
	e, err := core.NewEngine(store, p, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a := core.NewAgentRunner(context.Background(), e, model, timeout)
	t.Cleanup(a.Close)
	return e, a, p, store
}
func beginAgent(t *testing.T, a *core.AgentRunner, mode string) core.AgentRun {
	t.Helper()
	r, err := a.Start(core.AgentRequest{Namespace: "lab", Workload: "app", Mode: mode, Goal: "定位目标异常", MaxSteps: 4}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func awaitAgent(t *testing.T, a *core.AgentRunner, id string) core.AgentRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := a.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Terminal() || r.Status == "awaiting_approval" {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("agent did not reach a stable state")
	return core.AgentRun{}
}
func approveAgent(t *testing.T, a *core.AgentRunner, r core.AgentRun) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		err := a.Approve(r.ID, r.PlanHash)
		if err == nil {
			return
		}
		if !errors.Is(err, core.ErrBusy) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestAgentToolLoopApprovalAndVerifiedRecovery(t *testing.T) {
	e, a, p, store := agentSetup(t, testAgentModel{}, 0)
	r := awaitAgent(t, a, beginAgent(t, a, "approval").ID)
	if r.Status != "awaiting_approval" || r.ModelCalls != 3 || r.Diagnosis.Engine != "test:model" || r.PlanHash == "" || p.writes != 0 {
		t.Fatalf("unexpected analysis: %+v writes=%d", r, p.writes)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "private model reasoning") {
		t.Fatal("private model content persisted")
	}
	if !strings.Contains(string(b), "kb-ImagePullBackOff") {
		t.Fatal("missing versioned knowledge provenance")
	}
	if err := a.Approve(r.ID, "wrong-plan"); err == nil {
		t.Fatal("wrong approval accepted")
	}
	approveAgent(t, a, r)
	r = awaitAgent(t, a, r.ID)
	if r.Status != "recovered" || p.writes != 1 || e.State().Incidents[0].Status != "resolved" {
		t.Fatalf("not verified: %s writes=%d", r.Status, p.writes)
	}
	if err := a.Approve(r.ID, r.PlanHash); err == nil {
		t.Fatal("duplicate approval accepted")
	}
	saved, err := store.Load()
	if err != nil || saved.AgentRuns[0].Status != "recovered" {
		t.Fatal("run not persisted", err)
	}
}
func TestAgentAutomaticPoliciesAndModelFailure(t *testing.T) {
	for _, scenario := range []string{"success", "policy-off", "unavailable", "unauthorized", "fabrication", "loop"} {
		t.Run(scenario, func(t *testing.T) {
			modelMode := scenario
			if scenario == "success" || scenario == "policy-off" {
				modelMode = ""
			}
			e, a, p, _ := agentSetup(t, testAgentModel{mode: modelMode}, 0)
			if scenario != "policy-off" {
				if err := e.SetPolicy("restore_image", true, true); err != nil {
					t.Fatal(err)
				}
			}
			r := awaitAgent(t, a, beginAgent(t, a, "automatic").ID)
			if scenario == "success" {
				if r.Status != "recovered" || p.writes != 1 {
					t.Fatalf("auto repair failed %s", r.Status)
				}
			} else {
				if r.Status != "awaiting_approval" || p.writes != 0 {
					t.Fatalf("unapproved write: %+v writes=%d", r, p.writes)
				}
				if scenario != "policy-off" && !r.Fallback {
					t.Fatal("fallback not recorded")
				}
			}
		})
	}
}
func TestAgentObserveStalePlanAndUnconfirmedRecovery(t *testing.T) {
	t.Run("observe", func(t *testing.T) {
		_, a, p, _ := agentSetup(t, nil, 0)
		r := awaitAgent(t, a, beginAgent(t, a, "observe").ID)
		if r.Status != "completed" || r.Plan == nil || p.writes != 0 {
			t.Fatalf("observe mutated: %+v", r)
		}
	})
	t.Run("stale-plan", func(t *testing.T) {
		_, a, p, _ := agentSetup(t, nil, 0)
		r := awaitAgent(t, a, beginAgent(t, a, "approval").ID)
		p.changed = true
		approveAgent(t, a, r)
		r = awaitAgent(t, a, r.ID)
		if r.Status != "failed" || p.writes != 0 {
			t.Fatalf("stale plan executed: %+v", r)
		}
	})
	t.Run("business-unconfirmed", func(t *testing.T) {
		e, a, p, _ := agentSetup(t, nil, 0)
		p.healthy = false
		_ = e.SetPolicy("restore_image", true, true)
		r := awaitAgent(t, a, beginAgent(t, a, "automatic").ID)
		if r.Status != "needs_attention" || p.writes != 1 {
			t.Fatalf("false success: %+v", r)
		}
	})
}
func TestAgentScopeCancelTimeoutAndRestart(t *testing.T) {
	t.Run("scope-and-duplicate", func(t *testing.T) {
		_, a, p, _ := agentSetup(t, nil, 0)
		if _, err := a.Start(core.AgentRequest{Namespace: "kube-system", Workload: "app", Mode: "automatic"}, "test"); err == nil {
			t.Fatal("scope bypass")
		}
		r := awaitAgent(t, a, beginAgent(t, a, "approval").ID)
		if _, err := a.Start(r.Request, "test"); err == nil {
			t.Fatal("duplicate run allowed")
		}
		if err := a.Cancel(r.ID); err != nil {
			t.Fatal(err)
		}
		r = awaitAgent(t, a, r.ID)
		if r.Status != "cancelled" || p.writes != 0 {
			t.Fatal("cancel wrote")
		}
	})
	for _, scenario := range []string{"cancel", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			timeout := time.Second
			if scenario == "timeout" {
				timeout = 80 * time.Millisecond
			}
			_, a, p, _ := agentSetup(t, testAgentModel{mode: "timeout"}, timeout)
			r := beginAgent(t, a, "automatic")
			if scenario == "cancel" {
				if err := a.Cancel(r.ID); err != nil {
					t.Fatal(err)
				}
			}
			r = awaitAgent(t, a, r.ID)
			want := "cancelled"
			if scenario == "timeout" {
				want = "needs_attention"
			}
			if r.Status != want || p.writes != 0 {
				t.Fatalf("unexpected stop: %s writes=%d", r.Status, p.writes)
			}
		})
	}
	t.Run("restart", func(t *testing.T) {
		_, a, p, store := agentSetup(t, nil, 0)
		r := awaitAgent(t, a, beginAgent(t, a, "approval").ID)
		a.Close()
		e, err := core.NewEngine(store, p, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if e.State().AgentRuns[0].ID != r.ID || e.State().AgentRuns[0].Status != "interrupted" || p.writes != 0 {
			t.Fatal("restart replayed")
		}
	})
}
func TestAgentSchedulerCreatesAuditableTask(t *testing.T) {
	e, a, p, _ := agentSetup(t, nil, 0)
	_ = e.SetPolicy("restore_image", true, true)
	if err := a.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := e.State()
	if len(s.AgentRuns) != 1 || s.AgentRuns[0].Trigger != "scheduler" {
		t.Fatal("no scheduled task")
	}
	r := awaitAgent(t, a, s.AgentRuns[0].ID)
	if r.Status != "recovered" || p.writes != 1 {
		t.Fatal("scheduled recovery failed")
	}
}

type modelCallback struct {
	next func(context.Context, core.AgentInput) (core.AgentDecision, error)
}

func (m modelCallback) Name() string { return "test:callback" }
func (m modelCallback) Next(ctx context.Context, in core.AgentInput) (core.AgentDecision, error) {
	return m.next(ctx, in)
}

type versionedProvider struct {
	*controlledProvider
	version atomic.Int64
}

func (p *versionedProvider) Collect(ctx context.Context) (core.Snapshot, error) {
	s, err := p.controlledProvider.Collect(ctx)
	if p.version.Load() > 0 {
		s.Findings[0].Target.Version = "2"
	}
	return s, err
}
func TestAgentRejectsEvidenceChangedDuringModelAnalysis(t *testing.T) {
	p := &versionedProvider{controlledProvider: &controlledProvider{healthy: true}}
	e, err := core.NewEngine(core.FileStore{Path: filepath.Join(t.TempDir(), "state.json")}, p, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	model := modelCallback{next: func(ctx context.Context, in core.AgentInput) (core.AgentDecision, error) {
		if len(in.Exchanges) == 2 {
			p.version.Store(1)
			if err := e.Inspect(ctx, "concurrent-update"); err != nil {
				return core.AgentDecision{}, err
			}
		}
		return (testAgentModel{}).Next(ctx, in)
	}}
	a := core.NewAgentRunner(context.Background(), e, model, 0)
	defer a.Close()
	_ = e.SetPolicy("restore_image", true, true)
	r := awaitAgent(t, a, beginAgent(t, a, "automatic").ID)
	if r.Status != "failed" || p.writes != 0 || !strings.Contains(r.Summary, "版本已变化") {
		t.Fatalf("stale evidence accepted: %+v", r)
	}
}
func TestAgentConcurrentStartAndFailedIntentPersistence(t *testing.T) {
	t.Run("concurrent-start", func(t *testing.T) {
		_, a, _, _ := agentSetup(t, testAgentModel{mode: "timeout"}, 0)
		var wg sync.WaitGroup
		var successes atomic.Int32
		var runID string
		var mu sync.Mutex
		for j := 0; j < 12; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := a.Start(core.AgentRequest{Namespace: "lab", Workload: "app", Mode: "approval"}, "test")
				if err == nil {
					successes.Add(1)
					mu.Lock()
					runID = r.ID
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if successes.Load() != 1 {
			t.Fatal("multiple workers started", successes.Load())
		}
		if err := a.Cancel(runID); err != nil {
			t.Fatal(err)
		}
		if r := awaitAgent(t, a, runID); r.Status != "cancelled" {
			t.Fatal(r.Status)
		}
	})
	t.Run("intent-save-fails", func(t *testing.T) {
		p := &controlledProvider{healthy: true}
		store := &memoryStore{}
		e, err := core.NewEngine(store, p, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		a := core.NewAgentRunner(context.Background(), e, nil, 0)
		defer a.Close()
		store.fail = true
		if _, err := a.Start(core.AgentRequest{Namespace: "lab", Workload: "app", Mode: "automatic"}, "test"); err == nil {
			t.Fatal("unpersisted run started")
		}
		if len(e.State().AgentRuns) != 0 || p.writes != 0 {
			t.Fatal("side effect after persistence failure")
		}
	})
}

func TestAgentSchedulerCoolsDownTimedOutRuns(t *testing.T) {
	e, a, p, _ := agentSetup(t, testAgentModel{mode: "timeout"}, 80*time.Millisecond)
	_ = e.SetPolicy("restore_image", true, true)
	if err := a.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := awaitAgent(t, a, e.State().AgentRuns[0].ID)
	if r.Status != "needs_attention" {
		t.Fatal(r.Status)
	}
	for _, step := range r.Steps {
		if step.Status == "running" {
			t.Fatal("terminal run has active step")
		}
	}
	// Verify several ticks after worker cleanup without waiting five minutes.
	for j := 0; j < 3; j++ {
		time.Sleep(15 * time.Millisecond)
		if err := a.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.State().AgentRuns) != 1 || p.writes != 0 {
		t.Fatal("scheduler immediately resubmitted a timed-out run")
	}
}
