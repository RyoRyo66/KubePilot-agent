package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kubeagent/internal/core"
	"kubeagent/internal/providers"
)

type memoryStore struct {
	state *core.State
	fail  bool
}

func (m *memoryStore) Load() (core.State, error) {
	if m.state == nil {
		return core.State{}, os.ErrNotExist
	}
	return *m.state, nil
}
func (m *memoryStore) Save(s core.State) error {
	if m.fail {
		return errors.New("disk unavailable")
	}
	m.state = &s
	return nil
}

type controlledProvider struct {
	mu         sync.Mutex
	writes     int
	changed    bool
	collectErr error
	started    chan struct{}
	unblock    chan struct{}
	healthy    bool
}

func (p *controlledProvider) Collect(context.Context) (core.Snapshot, error) {
	return core.Snapshot{At: time.Now().UTC(), Findings: []core.Finding{{Key: "lab/app/image", Type: "ImagePullBackOff", Title: "镜像异常", Severity: "critical", Target: core.Target{Kind: "Deployment", Namespace: "lab", Name: "app", UID: "uid-1", Version: "1"}, Evidence: []core.Evidence{{ID: "e1", Detail: "manifest unknown"}}}}}, p.collectErr
}
func (p *controlledProvider) Plan(_ context.Context, i core.Incident) (*core.Plan, error) {
	after := "healthy"
	if p.changed {
		after = "changed"
	}
	return &core.Plan{Action: "restore_image", Target: i.Target, Before: "bad", After: after}, nil
}
func (p *controlledProvider) Execute(ctx context.Context, _ core.Plan) (string, error) {
	p.mu.Lock()
	p.writes++
	p.mu.Unlock()
	if p.started != nil {
		close(p.started)
		select {
		case <-p.unblock:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "submitted", nil
}
func (p *controlledProvider) Verify(context.Context, core.Incident) (bool, string, error) {
	return p.healthy, "business probe", nil
}
func setup(t *testing.T, p *controlledProvider, writes bool) (*core.Engine, *memoryStore, string) {
	t.Helper()
	store := &memoryStore{}
	e, err := core.NewEngine(store, p, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: writes})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Inspect(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	incidentID := e.State().Incidents[0].ID
	if err = e.Diagnose(context.Background(), incidentID); err != nil {
		t.Fatal(err)
	}
	return e, store, incidentID
}
func TestRejectsUnapprovedWrites(t *testing.T) {
	for _, scenario := range []string{"read-only", "policy-disabled", "stale-plan", "persist-failure"} {
		t.Run(scenario, func(t *testing.T) {
			p := &controlledProvider{healthy: true}
			e, store, id := setup(t, p, scenario != "read-only")
			switch scenario {
			case "policy-disabled":
				if err := e.SetPolicy("restore_image", false, false); err != nil {
					t.Fatal(err)
				}
			case "stale-plan":
				p.changed = true
			case "persist-failure":
				store.fail = true
			}
			if err := e.Execute(context.Background(), id, false); err == nil {
				t.Fatal("unsafe execution accepted")
			}
			if p.writes != 0 {
				t.Fatalf("unexpected external writes: %d", p.writes)
			}
		})
	}
}
func TestWriteSuccessDoesNotMeanRecovery(t *testing.T) {
	p := &controlledProvider{}
	e, _, id := setup(t, p, true)
	if err := e.Execute(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	i := e.State().Incidents[0]
	if i.Status != "manual" || i.ResolvedAt != nil {
		t.Fatal("unhealthy business was marked recovered")
	}
	if err := e.Execute(context.Background(), id, false); err == nil {
		t.Fatal("duplicate execution accepted")
	}
	if p.writes != 1 {
		t.Fatal("write was repeated")
	}
}
func TestConcurrentExecutionRunsOnce(t *testing.T) {
	p := &controlledProvider{healthy: true, started: make(chan struct{}), unblock: make(chan struct{})}
	e, _, id := setup(t, p, true)
	done := make(chan error, 1)
	go func() { done <- e.Execute(context.Background(), id, false) }()
	<-p.started
	if err := e.Execute(context.Background(), id, false); !errors.Is(err, core.ErrBusy) {
		t.Fatalf("expected busy: %v", err)
	}
	close(p.unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.writes != 1 {
		t.Fatal("more than one write")
	}
}
func TestCollectionFailureIsReportedAndCannotTriggerAutomation(t *testing.T) {
	p := &controlledProvider{healthy: true}
	e, _, _ := setup(t, p, true)
	if err := e.SetPolicy("restore_image", true, true); err != nil {
		t.Fatal(err)
	}
	p.collectErr = errors.New("API unavailable")
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("collection error hidden")
	}
	s := e.State()
	if s.Inspections[len(s.Inspections)-1].Status != "failed" || p.writes != 0 {
		t.Fatal("failed inspection executed automation")
	}
}
func TestDemoClosedLoopAndPersistence(t *testing.T) {
	dir := t.TempDir()
	demo, err := providers.NewDemo(filepath.Join(dir, "world.json"), "lab")
	if err != nil {
		t.Fatal(err)
	}
	store := core.FileStore{Path: filepath.Join(dir, "state.json")}
	settings := core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true}
	e, err := core.NewEngine(store, demo, nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = e.Inspect(context.Background(), "test"); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.State().Incidents) != 3 {
		t.Fatal("dedup failed")
	}
	var id string
	for _, i := range e.State().Incidents {
		if i.Occurrences != 2 {
			t.Fatal("observation count wrong")
		}
		if i.Type == "ImagePullBackOff" {
			id = i.ID
		}
	}
	if err = e.Diagnose(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err = e.Execute(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	demo2, err := providers.NewDemo(filepath.Join(dir, "world.json"), "lab")
	if err != nil {
		t.Fatal(err)
	}
	e2, err := core.NewEngine(store, demo2, nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range e2.State().Incidents {
		if i.ID == id && i.Status != "resolved" {
			t.Fatal("state not durable")
		}
	}
	snap, err := demo2.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range snap.Findings {
		if f.Type == "ImagePullBackOff" {
			t.Fatal("demo world not durable")
		}
	}
}
func TestInterruptedExecutionIsNotReplayed(t *testing.T) {
	p := &controlledProvider{}
	e, store, _ := setup(t, p, true)
	persisted := e.State()
	persisted.Incidents[0].Status = "executing"
	store.state = &persisted
	restarted, err := core.NewEngine(store, p, nil, persisted.Settings)
	if err != nil {
		t.Fatal(err)
	}
	i := restarted.State().Incidents[0]
	if i.Status != "manual" || i.Plan != nil || p.writes != 0 {
		t.Fatal("restart did not fail closed")
	}
}

func TestAutomaticPolicyClosesOnlyAllowedDemoFault(t *testing.T) {
	dir := t.TempDir()
	demo, err := providers.NewDemo(filepath.Join(dir, "world.json"), "lab")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewEngine(core.FileStore{Path: filepath.Join(dir, "state.json")}, demo, nil, core.Settings{Mode: "demo", Namespace: "lab", WritesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.SetPolicy("restore_image", true, true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = engine.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	imageEvents := 0
	for _, incident := range engine.State().Incidents {
		if incident.Type == "ImagePullBackOff" {
			imageEvents++
			if incident.Status != "resolved" || incident.ExecutionCount != 1 {
				t.Fatal("automatic repair did not close the loop")
			}
		} else if incident.ExecutionCount != 0 {
			t.Fatal("disabled automatic policy still executed")
		}
	}
	if imageEvents != 1 {
		t.Fatal("automatic repair was duplicated")
	}
}
func TestModeMismatchAndCorruptStateFailClosed(t *testing.T) {
	p := &controlledProvider{}
	_, store, _ := setup(t, p, true)
	_, err := core.NewEngine(store, p, nil, core.Settings{Mode: "kubernetes"})
	if err == nil {
		t.Fatal("environment mismatch accepted")
	}
	file := filepath.Join(t.TempDir(), "state.json")
	_ = os.WriteFile(file, []byte("broken"), 0600)
	_, err = core.NewEngine(core.FileStore{Path: file}, p, nil, core.Settings{Mode: "demo"})
	if err == nil || !strings.Contains(err.Error(), "读取状态失败") {
		t.Fatal("corrupt state silently discarded")
	}
}
