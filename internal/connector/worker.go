package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kubeagent/internal/core"
	"kubeagent/internal/platform"
	"kubeagent/internal/providers"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Worker struct {
	Config   platform.ConnectorConfig
	Provider interface {
		core.Provider
		Inspect(context.Context, core.Target, string) (core.CheckResult, error)
	}
	Kind      string
	ID        string
	dir       string
	journal   map[string]string
	Client    *http.Client
	Baselines map[string]string
}

func writeFile(path string, data []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".connector-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}
func New(config platform.ConnectorConfig, dir string, demo bool) (*Worker, error) {
	if err := platform.ValidatePublicURL(config.ServerURL, config.AllowHTTP); err != nil {
		return nil, err
	}
	if err := platform.ValidateScope(config.Scope); err != nil {
		return nil, err
	}
	if len(config.Token) < 32 || config.Generation < 1 {
		return nil, errors.New("执行端凭证或配置版本无效")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	w := &Worker{Config: config, dir: dir, journal: map[string]string{}, Baselines: map[string]string{}, Kind: "kubernetes", Client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if b, e := os.ReadFile(filepath.Join(dir, "journal.json")); e == nil {
		if json.Unmarshal(b, &w.journal) != nil {
			return nil, errors.New("执行日志损坏，拒绝启动以避免重复写入")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if b, e := os.ReadFile(filepath.Join(dir, "identity")); e == nil {
		w.ID = string(b)
	} else if errors.Is(e, os.ErrNotExist) {
		w.ID = fmt.Sprintf("connector-%d", time.Now().UnixNano())
		if e = writeFile(filepath.Join(dir, "identity"), []byte(w.ID)); e != nil {
			return nil, e
		}
	} else {
		return nil, e
	}
	var err error
	if demo {
		w.Kind = "demo"
		w.Provider, err = providers.NewDemo(filepath.Join(dir, "demo-world.json"), config.Scope.Namespace)
	} else {
		probes := map[string]string{}
		for _, t := range config.Scope.Targets {
			probes[t.Kind+"/"+t.Name] = t.ProbeURL
		}
		b, _ := json.Marshal(probes)
		path := filepath.Join(dir, "probes.json")
		if err = writeFile(path, b); err != nil {
			return nil, err
		}
		w.Provider, err = providers.NewKubernetes(providers.KubeConfig{Context: "in-cluster", Namespace: config.Scope.Namespace, BaselinePath: filepath.Join(dir, "baselines.json"), ProbeConfig: path})
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}
func (w *Worker) authorized(t core.Target, action string) bool {
	if t.Namespace != w.Config.Scope.Namespace {
		return false
	}
	target := false
	for _, v := range w.Config.Scope.Targets {
		if t.Kind == v.Kind && t.Name == v.Name {
			target = true
		}
	}
	if !target {
		return false
	}
	if action == "" {
		return true
	}
	for _, a := range w.Config.Scope.Actions {
		if a == action {
			return true
		}
	}
	return false
}
func (w *Worker) Handle(ctx context.Context, j platform.Job) platform.JobResult {
	r := platform.JobResult{ID: j.ID, Lease: j.Lease, ConnectorID: w.ID}
	fail := func(e error) platform.JobResult { r.Error = e.Error(); return r }
	if j.Generation != w.Config.Generation || j.Owner != w.ID || j.Lease == "" || j.ID == "" || time.Now().After(j.ExpiresAt) || time.Until(j.ExpiresAt) > 90*time.Second {
		return fail(errors.New("任务版本、租约或有效期不合法"))
	}
	ctx, cancel := context.WithDeadline(ctx, j.ExpiresAt)
	defer cancel()
	if j.Action != "collect" && (j.Incident == nil || !w.authorized(j.Incident.Target, "")) {
		return fail(errors.New("执行端拒绝未授权资源"))
	}
	switch j.Action {
	case "collect":
		snap, e := w.Provider.Collect(ctx)
		if e != nil {
			return fail(e)
		}
		filtered := []core.Finding{}
		for _, f := range snap.Findings {
			if w.authorized(f.Target, "") {
				filtered = append(filtered, f)
			}
		}
		snap.Findings = filtered
		r.Snapshot = &snap
		r.OK = true
	case "plan":
		p, e := w.Provider.Plan(ctx, *j.Incident)
		if e != nil {
			return fail(e)
		}
		if p != nil && !w.authorized(p.Target, p.Action) {
			return fail(errors.New("计划动作超出本地授权"))
		}
		r.Plan = p
		r.OK = true
	case "execute":
		if j.Plan == nil || j.PlanHash != core.PlanHash(j.Plan) || j.Plan.Target != j.Incident.Target || j.Plan.BaselineID == "" || !w.authorized(j.Plan.Target, j.Plan.Action) || j.Automatic && !w.Config.Scope.Automatic {
			return fail(errors.New("执行端计划或动作检查未通过"))
		}
		if _, exists := w.journal[j.ID]; exists {
			return fail(errors.New("任务曾经提交执行，拒绝重放"))
		}
		if len(w.journal) >= 10000 {
			return fail(errors.New("执行记录已达上限，请管理员归档后重新登记"))
		}
		fresh, e := w.Provider.Plan(ctx, *j.Incident)
		if e != nil {
			return fail(e)
		}
		if core.PlanHash(fresh) != j.PlanHash {
			return fail(errors.New("执行前对象或基线已变化"))
		}
		w.journal[j.ID] = j.PlanHash
		b, _ := json.Marshal(w.journal)
		if e = writeFile(filepath.Join(w.dir, "journal.json"), b); e != nil {
			delete(w.journal, j.ID)
			return fail(e)
		}
		detail, e := w.Provider.Execute(ctx, *j.Plan)
		if e != nil {
			return fail(e)
		}
		r.OK = true
		r.Detail = detail
	case "verify":
		ok, detail, e := w.Provider.Verify(ctx, *j.Incident)
		if e != nil {
			return fail(e)
		}
		r.OK = ok
		r.Detail = detail
	default:
		if j.Incident == nil || !slices.Contains(core.DiagnosticChecks(j.Incident.Target.Kind), j.Action) {
			return fail(errors.New("任务不在固定工具白名单中"))
		}
		check, err := w.Provider.Inspect(ctx, j.Incident.Target, j.Action)
		if err != nil {
			return fail(err)
		}
		r.Check, r.OK = &check, true
	}
	return r
}
func (w *Worker) post(ctx context.Context, path string, body, out any) error {
	b, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(w.Config.ServerURL, "/")+path, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+w.Config.Token)
	req.Header.Set("Content-Type", "application/json")
	res, e := w.Client.Do(req)
	if e != nil {
		return errors.New("平台不可达，请检查网络和地址")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("平台返回 HTTP %d，检查接入版本与令牌", res.StatusCode)
	}
	d := json.NewDecoder(io.LimitReader(res.Body, 2<<20))
	return d.Decode(out)
}
func (w *Worker) RefreshBaselines(ctx context.Context) {
	for _, t := range w.Config.Scope.Targets {
		key := t.Kind + "/" + t.Name
		if w.Kind == "demo" {
			w.Baselines[key] = "ready"
			continue
		}
		k := w.Provider.(*providers.Kubernetes)
		if e := k.EnsureBaseline(ctx, t.Kind, t.Name); e != nil {
			w.Baselines[key] = e.Error()
		} else {
			w.Baselines[key] = "ready"
		}
	}
}
func (w *Worker) Run(ctx context.Context, onError func(error)) error {
	nextBaseline := time.Time{}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(nextBaseline) {
			bc, cancel := context.WithTimeout(ctx, 35*time.Second)
			w.RefreshBaselines(bc)
			cancel()
			nextBaseline = time.Now().Add(30 * time.Second)
		}
		var reply platform.PollReply
		e := w.post(ctx, "/connector/poll", platform.Poll{Connection: platform.Connection{ID: w.ID, Namespace: w.Config.Scope.Namespace, Provider: w.Kind, Generation: w.Config.Generation, Baselines: w.Baselines}}, &reply)
		if e != nil {
			onError(e)
		} else if reply.Job != nil {
			result := w.Handle(ctx, *reply.Job)
			var ack any
			for attempt := 0; attempt < 3; attempt++ {
				e = w.post(ctx, "/connector/result", result, &ack)
				if e == nil {
					break
				}
				onError(e)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
