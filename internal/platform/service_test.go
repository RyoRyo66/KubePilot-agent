package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"kubeagent/internal/core"
	"kubeagent/internal/providers"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sigs.k8s.io/yaml"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testModel struct {
	decision string
	failed   bool
}

func (m testModel) Probe(context.Context) error {
	if m.failed {
		return errors.New("fixture unavailable")
	}
	return nil
}
func (m testModel) Investigate(_ context.Context, i core.Incident) (core.Investigation, error) {
	d := core.RuleDiagnosis(i)
	d.Disposition = "consider_plan"
	first := core.Investigation{Assessment: &d, InitialEvidence: i.Evidence, Engine: "test-model"}
	switch m.decision {
	case "inspect", "uncertain", "object-replaced", "bad-check", "no-new-citation", "repeat-check":
		d.Disposition = "inspect"
		first.Checks = []string{"service_backends"}
		first.Hypotheses = []core.Hypothesis{{Cause: "selector or pod readiness", Check: "compare ready backends", EvidenceIDs: d.EvidenceIDs}}
		if m.decision == "bad-check" {
			first.Checks = []string{"execute"}
		}
	}
	return first, nil
}
func (m testModel) AssessChecks(_ context.Context, i core.Incident, first core.Investigation, freshIDs []string) (core.CheckDecision, error) {
	if len(first.InitialEvidence) == 0 {
		return core.CheckDecision{}, errors.New("missing first observation")
	}
	d := core.RuleDiagnosis(i)
	d.Disposition = "consider_plan"
	d.EvidenceIDs = freshIDs
	if m.decision == "repeat-check" {
		d.Disposition = "inspect"
		return core.CheckDecision{Diagnosis: d, Checks: []string{"service_backends"}}, nil
	}
	if m.decision == "no-new-citation" {
		d.EvidenceIDs = []string{first.InitialEvidence[0].ID}
	}
	if m.decision == "uncertain" {
		d.Disposition = "need_evidence"
	}
	return core.CheckDecision{Diagnosis: d}, nil
}
func (m testModel) Review(_ context.Context, i core.Incident, p core.Plan) (core.Review, error) {
	if m.failed {
		return core.Review{}, errors.New("fixture review unavailable")
	}
	ids := []string{}
	for _, e := range i.Evidence {
		ids = append(ids, e.ID)
	}
	decision := m.decision
	if decision == "inspect" {
		decision = "approve"
	}
	return core.Review{Decision: decision, Reason: "test fixture review", PlanHash: core.PlanHash(&p), EvidenceIDs: ids, At: time.Now().UTC()}, nil
}
func testScope() Scope {
	return Scope{Namespace: "lab", Targets: []TargetSpec{{Kind: "Service", Name: "payment-service", ProbeURL: "http://payment-service/health"}}, Actions: []string{"restore_selector"}, Automatic: true, Image: "kubepilot-connector:0.2.0"}
}
func setup(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	s.factory = func(Config) (ModelClient, error) { return testModel{decision: "approve"}, nil }
	if _, e = s.SaveSource("http://localhost:18081", true); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveModel(context.Background(), "http://localhost:18083/v1", "fixture-model", "private-model-key"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveScope(testScope()); e != nil {
		t.Fatal(e)
	}
	return s, dir
}
func TestSecretsAndScopeManifest(t *testing.T) {
	s, dir := setup(t)
	st := s.current()
	b, _ := json.Marshal(s.View())
	for _, secret := range []string{st.Config.APIKey, st.Config.WebhookToken, st.Config.ConnectorToken} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("secret exposed by view")
		}
		disk, _ := os.ReadFile(filepath.Join(dir, "platform.enc"))
		if bytes.Contains(disk, []byte(secret)) {
			t.Fatal("secret stored as plaintext")
		}
	}
	text, e := s.Manifest()
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(text, "private-model-key") {
		t.Fatal("model key in manifest")
	}
	parts := strings.Split(text, "---\n")
	if len(parts) != 6 {
		t.Fatalf("manifest parts=%d", len(parts))
	}
	for _, part := range parts {
		var obj map[string]any
		if e = yaml.Unmarshal([]byte(part), &obj); e != nil {
			t.Fatal(e)
		}
		kind, _ := obj["kind"].(string)
		if kind == "ClusterRole" || kind == "ClusterRoleBinding" {
			t.Fatal("cluster-wide authorization")
		}
		if kind == "Role" {
			raw, _ := json.Marshal(obj)
			if bytes.Contains(raw, []byte(`"secrets"`)) || bytes.Contains(raw, []byte(`"delete"`)) || bytes.Contains(raw, []byte(`"*"`)) {
				t.Fatal("excessive permissions")
			}
			if !bytes.Contains(raw, []byte(`"resourceNames":["payment-service"]`)) {
				t.Fatal("write permissions not name-scoped")
			}
		}
	}
	for _, ns := range []string{"default", "kube-system", "kube-public", "bad name"} {
		scope := testScope()
		scope.Namespace = ns
		if ValidateScope(scope) == nil {
			t.Fatal("accepted namespace", ns)
		}
	}
	scope := testScope()
	scope.Targets[0].Name = "kubepilot-connector"
	if ValidateScope(scope) == nil {
		t.Fatal("executor can modify itself")
	}
}
func TestAuthSeparationAndWebhookDedup(t *testing.T) {
	s, _ := setup(t)
	h := s.Handler(t.TempDir(), "admin-test-token")
	st := s.current()
	request := func(path, token string, body any, app bool) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "http://localhost"+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		if app {
			r.Header.Set("X-Requested-With", "KubePilot")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("/connector/poll", st.Config.WebhookToken, Poll{}, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("/api/platform/scan", st.Config.ConnectorToken, map[string]any{}, true); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("/ingest/alertmanager", "admin-test-token", map[string]any{}, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	payload := []byte(`{"version":"4","status":"firing","groupKey":"x","commonLabels":{},"alerts":[{"status":"firing","labels":{"alertname":"AppUnavailable","namespace":"lab"},"annotations":{"summary":"example"},"startsAt":"2026-11-12T01:00:00Z","fingerprint":"same"}]}`)
	var p alertPayload
	if e := json.Unmarshal(payload, &p); e != nil {
		t.Fatal(e)
	}
	if e := s.Ingest(p); e != nil {
		t.Fatal(e)
	}
	p.Alerts[0].Status = "resolved"
	if e := s.Ingest(p); e != nil {
		t.Fatal(e)
	}
	state := s.current()
	if len(state.Alerts) != 1 || state.Alerts[0].Count != 2 || len(state.Cases) != 0 {
		t.Fatal("notifications were not deduped or became verified incidents")
	}
	p.Version = "3"
	if s.Ingest(p) == nil {
		t.Fatal("accepted unsupported webhook")
	}
}
func TestLeaseNotReplayedAndRestartInvalidates(t *testing.T) {
	s, dir := setup(t)
	cfg := s.current().Config
	connection := Connection{ID: "worker", Namespace: "lab", Provider: "demo", Generation: cfg.Generation, Baselines: map[string]string{}}
	_ = s.change(func(st *DiskState) error {
		st.Jobs = append(st.Jobs, Job{ID: "single", Status: "queued", Action: "execute", Generation: cfg.Generation, ExpiresAt: time.Now().Add(time.Minute)})
		return nil
	})
	first, e := s.Poll(Poll{connection})
	if e != nil || first.Job == nil {
		t.Fatal(e)
	}
	again, e := s.Poll(Poll{connection})
	if e != nil || again.Job != nil {
		t.Fatal("leased task was delivered twice")
	}
	if s.Result(JobResult{ID: "single", Lease: "wrong", ConnectorID: "worker"}) == nil {
		t.Fatal("accepted forged lease")
	}
	restored, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	reply, e := restored.Poll(Poll{connection})
	if e != nil || reply.Job != nil {
		t.Fatal("restart replayed an uncertain write")
	}
	oldToken := cfg.ConnectorToken
	if _, e = s.SaveScope(testScope()); e != nil {
		t.Fatal(e)
	}
	if s.authenticated(oldToken, true) {
		t.Fatal("old enrollment token remained valid")
	}
}
func TestAutomaticPipelineAndReviewStops(t *testing.T) {
	for _, scenario := range []struct {
		name, decision string
		reviewFail     bool
		wantWrites     int32
		wantStatus     string
	}{{"approved", "approve", false, 1, "resolved"}, {"rejected", "reject", false, 0, "rejected"}, {"manual", "manual", false, 0, "manual"}, {"unavailable", "approve", true, 0, "manual"}, {"insufficient", "uncertain", false, 0, "manual"}, {"object-replaced", "object-replaced", false, 0, "manual"}, {"investigated", "inspect", false, 1, "resolved"}, {"forbidden-check", "bad-check", false, 0, "manual"}, {"ignored-new-evidence", "no-new-citation", false, 0, "manual"}, {"repeat-check", "repeat-check", false, 0, "manual"}} {
		t.Run(scenario.name, func(t *testing.T) {
			s, _ := setup(t)
			s.factory = func(Config) (ModelClient, error) {
				return testModel{decision: scenario.decision, failed: scenario.reviewFail}, nil
			}
			provider, e := providers.NewDemo(filepath.Join(t.TempDir(), "world.json"), "lab")
			if e != nil {
				t.Fatal(e)
			}
			if e = provider.Reset(); e != nil {
				t.Fatal(e)
			}
			if e = provider.Inject("ServiceSelector"); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var writes atomic.Int32
			var collects atomic.Int32
			var checks atomic.Int32
			done := make(chan struct{})
			cfg := s.current().Config
			connection := Connection{ID: "fixture", Namespace: "lab", Provider: "demo", Generation: cfg.Generation, Baselines: map[string]string{"Service/payment-service": "ready"}}
			_, _ = s.Poll(Poll{connection})
			go func() {
				defer close(done)
				for {
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Millisecond):
					}
					poll, err := s.Poll(Poll{connection})
					if err != nil || poll.Job == nil {
						continue
					}
					j := poll.Job
					r := JobResult{ID: j.ID, Lease: j.Lease, ConnectorID: "fixture"}
					switch j.Action {
					case "collect":
						v, e := provider.Collect(ctx)
						collects.Add(1)
						r.Snapshot = &v
						err = e
					case "service_backends":
						checks.Add(1)
						result, e := provider.Inspect(ctx, j.Incident.Target, j.Action)
						if scenario.name == "object-replaced" {
							result.Target.UID = "replacement-uid"
						}
						r.Check, r.OK, err = &result, e == nil, e
					case "plan":
						r.Plan, err = provider.Plan(ctx, *j.Incident)
					case "execute":
						writes.Add(1)
						r.Detail, err = provider.Execute(ctx, *j.Plan)
						r.OK = err == nil
					case "verify":
						r.OK, r.Detail, err = provider.Verify(ctx, *j.Incident)
					}
					if err != nil {
						r.Error = err.Error()
					}
					_ = s.Result(r)
				}
			}()
			if e = s.Scan(); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(12 * time.Second)
			for s.busy() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if s.busy() {
				t.Fatal("pipeline did not finish")
			}
			state := s.current()
			if len(state.Cases) != 1 {
				t.Fatalf("cases=%d, error=%s", len(state.Cases), state.LastError)
			}
			c := state.Cases[0]
			if collects.Load() != 1 {
				t.Fatal("repeated bulk collection")
			}
			wantsCheck := scenario.name == "investigated" || scenario.name == "insufficient" || scenario.name == "object-replaced" || scenario.name == "ignored-new-evidence" || scenario.name == "repeat-check"
			if (checks.Load() == 1) != wantsCheck {
				t.Fatal("wrong diagnostic path")
			}
			if scenario.name != "forbidden-check" && c.Investigation == nil {
				t.Fatal("missing first assessment")
			}
			if scenario.name == "insufficient" || scenario.name == "object-replaced" || scenario.name == "forbidden-check" || scenario.name == "ignored-new-evidence" || scenario.name == "repeat-check" {
				for _, j := range state.Jobs {
					if j.Action != "collect" && j.Action != "service_backends" {
						t.Fatalf("dispatched %s despite inconclusive evidence", j.Action)
					}
				}
				if c.Review != nil || c.Incident.Plan != nil {
					t.Fatal("uncertain investigation reached planning")
				}
			}
			if c.Incident.Status != scenario.wantStatus || writes.Load() != scenario.wantWrites {
				t.Fatalf("status=%s writes=%d timeline=%+v", c.Incident.Status, writes.Load(), c.Incident.Timeline)
			}
			if scenario.wantWrites == 1 {
				if e = s.Scan(); e != nil {
					t.Fatal(e)
				}
				for s.busy() {
					time.Sleep(10 * time.Millisecond)
				}
				if writes.Load() != 1 {
					t.Fatal("duplicate execution")
				}
			}
			cancel()
			<-done
		})
	}
}

func TestExpiredReviewAndChangedPlanCannotExecute(t *testing.T) {
	s, _ := setup(t)
	p := core.Plan{Action: "restore_selector", BaselineID: "known", Target: core.Target{Kind: "Service", Namespace: "lab", Name: "payment-service", UID: "uid", Version: "1"}, Before: "old", After: "new"}
	hash := core.PlanHash(&p)
	_ = s.change(func(st *DiskState) error {
		st.Cases = append(st.Cases, Case{Generation: st.Config.Generation, Incident: core.Incident{ID: "case", Plan: &p, Target: p.Target}, Review: &core.Review{Decision: "approve", PlanHash: hash}, ReviewExpires: time.Now().Add(-time.Second)})
		return nil
	})
	if e := s.execute(context.Background(), "case", hash, true); e == nil {
		t.Fatal("expired review executed")
	}
	if e := s.execute(context.Background(), "case", "changed", true); e == nil {
		t.Fatal("changed plan executed")
	}
	if len(s.current().Jobs) != 0 {
		t.Fatal("invalid approval issued a connector task")
	}
}

func TestWriteRequiresBrowserHeaderAndSameOrigin(t *testing.T) {
	s, _ := setup(t)
	h := s.Handler(t.TempDir(), "")
	for _, origin := range []string{"", "https://evil.invalid"} {
		r := httptest.NewRequest("POST", "http://localhost/api/platform/pause", strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
			r.Header.Set("X-Requested-With", "KubePilot")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal(w.Code)
		}
	}
}

func TestPauseStopsQueuedAutomaticWrite(t *testing.T) {
	s, _ := setup(t)
	cfg := s.current().Config
	connection := Connection{ID: "fixture", Namespace: "lab", Provider: "demo", Generation: cfg.Generation}
	_, err := s.Poll(Poll{connection})
	if err != nil {
		t.Fatal(err)
	}
	err = s.change(func(st *DiskState) error {
		st.Jobs = append(st.Jobs, Job{ID: "queued-write", Action: "execute", Status: "queued", Automatic: true, Generation: cfg.Generation, ExpiresAt: time.Now().Add(time.Minute)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://localhost/api/platform/pause", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "KubePilot")
	w := httptest.NewRecorder()
	s.Handler(t.TempDir(), "").ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	poll, err := s.Poll(Poll{connection})
	if err != nil || poll.Job != nil || s.current().Jobs[0].Status != "interrupted" {
		t.Fatalf("paused automatic job was not stopped: %+v %v", poll, err)
	}
	if _, err = s.call(context.Background(), "execute", nil, nil, true); err == nil {
		t.Fatal("new automatic write accepted after pause")
	}
}

func TestExternalRecoveryRequiresReadOnlyVerificationAndPreservesHistory(t *testing.T) {
	for _, scenario := range []string{"verified", "not-verified", "still-present", "write-attempted", "changed-generation"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := setup(t)
			cfg := s.current().Config
			incident := core.Incident{ID: "external", Key: "service-key", Type: "ServiceSelector", Status: "manual", Target: core.Target{Kind: "Service", Namespace: "lab", Name: "payment-service", UID: "service-uid"}, Timeline: []core.Step{{Status: "manual", Title: "Original diagnosis rejected"}}}
			if scenario == "write-attempted" {
				incident.ExecutionCount = 1
			}
			generation := cfg.Generation
			if scenario == "changed-generation" {
				generation--
			}
			if err := s.change(func(st *DiskState) error {
				st.Cases = []Case{{Generation: generation, Incident: incident, ReviewExpires: time.Now().Add(time.Hour)}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			connection := Connection{ID: "readonly-check", Namespace: "lab", Provider: "kubernetes", Generation: cfg.Generation}
			if _, err := s.Poll(Poll{connection}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var verifies, writes atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Millisecond):
					}
					poll, err := s.Poll(Poll{connection})
					if err != nil || poll.Job == nil {
						continue
					}
					job := poll.Job
					r := JobResult{ID: job.ID, Lease: job.Lease, ConnectorID: connection.ID, OK: true}
					switch job.Action {
					case "collect":
						r.Snapshot = &core.Snapshot{At: time.Now()}
						if scenario == "still-present" {
							r.Snapshot.Findings = []core.Finding{{Key: incident.Key, Type: incident.Type, Target: incident.Target}}
						}
					case "verify":
						verifies.Add(1)
						r.OK = scenario == "verified"
						r.Detail = "live endpoint and HTTP verification"
					default:
						writes.Add(1)
						r.Error = "unexpected mutating job"
					}
					_ = s.Result(r)
				}
			}()
			if err := s.Scan(); err != nil {
				t.Fatal(err)
			}
			for s.busy() && ctx.Err() == nil {
				time.Sleep(time.Millisecond)
			}
			cancel()
			<-done
			if s.busy() {
				t.Fatal("scan did not finish")
			}
			result := s.current().Cases[0]
			if writes.Load() != 0 {
				t.Fatal("recovery confirmation dispatched a write")
			}
			if scenario == "verified" {
				if result.Incident.Status != "resolved" || result.Incident.ExecutionCount != 0 || result.Incident.ResolvedAt == nil || !result.ReviewExpires.IsZero() || verifies.Load() != 1 {
					t.Fatal("verified external recovery not recorded correctly")
				}
				if result.Incident.Timeline[0].Title != "Original diagnosis rejected" || !strings.Contains(result.Incident.Timeline[1].Detail, "未执行平台写操作") {
					t.Fatal("failure history or external recovery attribution lost")
				}
			} else if result.Incident.Status != "manual" {
				t.Fatal("unverified recovery was accepted")
			}
			if scenario != "verified" && scenario != "not-verified" && verifies.Load() != 0 {
				t.Fatal("verified an active, written or stale-generation case")
			}
		})
	}
}

func TestFailedModelProbePreservesConfiguration(t *testing.T) {
	s, _ := setup(t)
	before := s.current().Config
	s.factory = func(Config) (ModelClient, error) { return testModel{failed: true}, nil }
	if err := s.SaveModel(context.Background(), "https://invalid.example", "changed", "changed-key"); err == nil {
		t.Fatal("unavailable model saved")
	}
	after := s.current().Config
	if before.APIKey != after.APIKey || before.Model != after.Model || before.BaseURL != after.BaseURL || !after.ModelTested {
		t.Fatal("failed connection test changed the active configuration")
	}
}
