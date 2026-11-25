package connector

import (
	"context"
	"kubeagent/internal/core"
	"kubeagent/internal/platform"
	"kubeagent/internal/providers"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalExecutorScopeAndDurableReplayGuard(t *testing.T) {
	cfg := platform.ConnectorConfig{ServerURL: "http://localhost:18081", AllowHTTP: true, Token: "connector-test-token-at-least-32-bytes", Generation: 1, Scope: platform.Scope{Namespace: "lab", Image: "connector:test", Automatic: true, Actions: []string{"restore_selector"}, Targets: []platform.TargetSpec{{Kind: "Service", Name: "payment-service", ProbeURL: "http://payment/health"}}}}
	dir := t.TempDir()
	w, e := New(cfg, dir, true)
	if e != nil {
		t.Fatal(e)
	}
	d := w.Provider.(*providers.Demo)
	_ = d.Reset()
	_ = d.Inject("ServiceSelector")
	snap, e := d.Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var incident core.Incident
	for _, f := range snap.Findings {
		if f.Type == "ServiceSelector" {
			incident = core.Incident{Type: f.Type, Target: f.Target}
		}
	}
	plan, e := d.Plan(context.Background(), incident)
	if e != nil {
		t.Fatal(e)
	}
	job := platform.Job{ID: "once", Action: "execute", Owner: w.ID, Lease: "lease", Generation: 1, ExpiresAt: time.Now().Add(time.Minute), Incident: &incident, Plan: plan, PlanHash: core.PlanHash(plan), Automatic: true}
	wrong := job
	wrong.PlanHash = "changed"
	if r := w.Handle(context.Background(), wrong); r.Error == "" {
		t.Fatal("executed tampered plan")
	}
	outside := incident
	outside.Target.Name = "other"
	wrong = job
	wrong.Incident = &outside
	if r := w.Handle(context.Background(), wrong); r.Error == "" {
		t.Fatal("executed outside scope")
	}
	result := w.Handle(context.Background(), job)
	if result.Error != "" || !result.OK {
		t.Fatal(result)
	}
	if r := w.Handle(context.Background(), job); r.Error == "" {
		t.Fatal("replayed write")
	}
	restarted, e := New(cfg, dir, true)
	if e != nil {
		t.Fatal(e)
	}
	if r := restarted.Handle(context.Background(), job); r.Error == "" {
		t.Fatal("replayed write after restart")
	}
	if filepath.Base(restarted.dir) == "" {
		t.Fatal("missing durable directory")
	}
}
