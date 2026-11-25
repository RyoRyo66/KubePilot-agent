package platform

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"kubeagent/internal/core"
	"kubeagent/internal/providers"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Service struct {
	library *knowledgeLibrary
	mu      sync.Mutex
	state   DiskState
	store   *encryptedStore
	factory ModelFactory
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func New(dir string) (*Service, error) {
	store, err := newStore(dir)
	if err != nil {
		return nil, err
	}
	state, err := store.load()
	if errors.Is(err, os.ErrNotExist) {
		state = DiskState{Schema: 1, Config: Config{WebhookToken: randomID(), ConnectorToken: randomID()}, Alerts: []Alert{}, Cases: []Case{}, Jobs: []Job{}, Events: []Event{}}
	} else if err != nil {
		return nil, err
	}
	for i := range state.Jobs {
		if state.Jobs[i].Status == "queued" || state.Jobs[i].Status == "leased" {
			state.Jobs[i].Status = "interrupted"
		}
	}
	for i := range state.Cases {
		switch state.Cases[i].Incident.Status {
		case "diagnosing", "reviewing", "executing", "verifying":
			state.Cases[i].Incident.Status = "manual"
			state.Cases[i].Incident.Timeline = append(state.Cases[i].Incident.Timeline, core.Step{At: time.Now().UTC(), Status: "manual", Title: "服务重启，任务已中断", Detail: "不重放写操作，请核对集群实际状态。"})
		}
	}
	// A persisted heartbeat never proves the connector is currently connected.
	state.Connection.LastSeen = time.Time{}
	if err = store.save(state); err != nil {
		return nil, err
	}
	library, err := openKnowledgeLibrary(dir, store)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{state: state, store: store, library: library, ctx: ctx, cancel: cancel, factory: func(c Config) (ModelClient, error) {
		m, err := providers.NewCompatible(c.BaseURL, c.Model, c.APIKey)
		if err != nil {
			return nil, err
		}
		m.Knowledge = library.snapshot()
		return m, nil
	}}
	return s, nil
}
func (s *Service) Close() { s.cancel(); s.wg.Wait() }
func (s *Service) change(fn func(*DiskState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.store.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}
func (s *Service) current() DiskState { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.state) }
func event(st *DiskState, action, detail string) {
	st.Events = append(st.Events, Event{ID: randomID()[:16], At: time.Now().UTC(), Action: action, Detail: detail})
	if len(st.Events) > 300 {
		st.Events = st.Events[len(st.Events)-300:]
	}
}
func (s *Service) busy() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.running }
func (s *Service) checkIdle() error {
	if s.running {
		return errors.New("任务运行中，请等待结束后修改接入配置")
	}
	return nil
}
func (s *Service) authenticated(token string, connector bool) bool {
	st := s.current()
	expected := st.Config.WebhookToken
	if connector {
		expected = st.Config.ConnectorToken
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
var imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,255}$`)

func ValidatePublicURL(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("平台地址应为完整域名或 IP 与端口，不包含路径、凭证或查询参数")
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return errors.New("平台地址需要 HTTPS；本地实验可明确允许 HTTP")
	}
	return nil
}
func ValidateScope(scope Scope) error {
	if !namePattern.MatchString(scope.Namespace) || len(scope.Namespace) > 63 || scope.Namespace == "default" || strings.HasPrefix(scope.Namespace, "kube-") && scope.Namespace != "kube-agent-lab" {
		return errors.New("请使用独立业务或实验命名空间，不允许 default 或 Kubernetes 系统命名空间")
	}
	if len(scope.Targets) < 1 || len(scope.Targets) > 20 || !imagePattern.MatchString(scope.Image) {
		return errors.New("请填写 1–20 个授权资源以及有效的 Connector 镜像地址")
	}
	seen := map[string]bool{}
	for _, t := range scope.Targets {
		if t.Kind != "Deployment" && t.Kind != "Service" || !namePattern.MatchString(t.Name) || len(t.Name) > 253 || strings.HasPrefix(t.Name, "kubepilot-") {
			return errors.New("授权资源必须为 Deployment 或 Service，名称必须有效")
		}
		key := t.Kind + "/" + t.Name
		if seen[key] {
			return errors.New("授权资源重复")
		}
		seen[key] = true
		u, err := url.Parse(t.ProbeURL)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("每个授权资源都需要 Connector 可访问的 HTTP(S) 业务健康检查地址")
		}
	}
	if len(scope.Actions) == 0 || len(scope.Actions) > 3 {
		return errors.New("请选择允许执行的恢复动作")
	}
	seen = map[string]bool{}
	for _, a := range scope.Actions {
		if a != "restore_image" && a != "restore_selector" && a != "restore_probe" || seen[a] {
			return errors.New("恢复动作无效或重复")
		}
		seen[a] = true
	}
	return nil
}
func (s *Service) SaveSource(raw string, allow bool) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if err := ValidatePublicURL(raw, allow); err != nil {
		return "", err
	}
	err := s.change(func(st *DiskState) error {
		if err := s.checkIdle(); err != nil {
			return err
		}
		if st.Config.Generation > 0 && st.Config.PublicURL != raw {
			return errors.New("Connector 已配置；平台地址变更需要重新部署平台及 Connector，本版不支持在线迁移")
		}
		st.Config.PublicURL = raw
		st.Config.AllowHTTP = allow
		event(st, "source.configured", "已保存 Alertmanager 接收地址")
		return nil
	})
	return s.SourceSnippet(), err
}
func (s *Service) SourceSnippet() string {
	c := s.current().Config
	return fmt.Sprintf("receivers:\n  - name: kubepilot\n    webhook_configs:\n      - url: %q\n        send_resolved: true\n        http_config:\n          authorization:\n            type: Bearer\n            credentials: %q\n# 将现有告警路由的 receiver 指向 kubepilot，然后重新加载 Alertmanager。\n", c.PublicURL+"/ingest/alertmanager", c.WebhookToken)
}
func (s *Service) SaveModel(ctx context.Context, base, model, key string) error {
	st := s.current()
	if key == "" {
		key = st.Config.APIKey
	}
	next := st.Config
	next.BaseURL = strings.TrimSpace(base)
	next.Model = strings.TrimSpace(model)
	next.APIKey = key
	m, err := s.factory(next)
	if err != nil {
		return err
	}
	if err = m.Probe(ctx); err != nil {
		return err
	}
	return s.change(func(st *DiskState) error {
		if err := s.checkIdle(); err != nil {
			return err
		}
		st.Config.BaseURL = next.BaseURL
		st.Config.Model = next.Model
		st.Config.APIKey = next.APIKey
		st.Config.ModelTested = true
		for i := range st.Cases {
			st.Cases[i].Review = nil
			st.Cases[i].ReviewExpires = time.Time{}
		}
		event(st, "model.connected", "模型通过实际工具调用测试；后续诊断与复核使用独立上下文")
		return nil
	})
}
func (s *Service) SaveScope(scope Scope) (string, error) {
	if err := ValidateScope(scope); err != nil {
		return "", err
	}
	err := s.change(func(st *DiskState) error {
		if err := s.checkIdle(); err != nil {
			return err
		}
		if st.Config.PublicURL == "" {
			return errors.New("请先保存平台接收地址")
		}
		st.Config.Scope = scope
		st.Config.Generation++
		st.Config.ConnectorToken = randomID()
		st.Connection = Connection{}
		for i := range st.Cases {
			st.Cases[i].Review = nil
			st.Cases[i].ReviewExpires = time.Time{}
		}
		event(st, "connector.authorized", "已更新指定资源与动作授权；旧 Connector 凭证失效，请应用新的部署清单")
		return nil
	})
	if err != nil {
		return "", err
	}
	return s.Manifest()
}
func (s *Service) View() map[string]any {
	st := s.current()
	online := st.Connection.Generation == st.Config.Generation && st.Config.Generation > 0 && !st.Connection.LastSeen.IsZero() && time.Since(st.Connection.LastSeen) < 90*time.Second
	ready := 0
	for _, t := range st.Config.Scope.Targets {
		if st.Connection.Baselines[t.Kind+"/"+t.Name] == "ready" {
			ready++
		}
	}
	return map[string]any{"version": Version, "config": map[string]any{"publicUrl": st.Config.PublicURL, "allowHttp": st.Config.AllowHTTP, "baseUrl": st.Config.BaseURL, "model": st.Config.Model, "apiKeySet": st.Config.APIKey != "", "modelTested": st.Config.ModelTested, "scope": st.Config.Scope, "generation": st.Config.Generation}, "connection": st.Connection, "online": online, "baselineReady": ready, "ready": online && st.Config.ModelTested && ready == len(st.Config.Scope.Targets), "busy": s.busy(), "alerts": st.Alerts, "cases": st.Cases, "events": st.Events, "snapshot": st.Snapshot, "lastScan": st.LastScan, "lastError": st.LastError}
}

func (s *Service) Poll(p Poll) (PollReply, error) {
	var reply PollReply
	err := s.change(func(st *DiskState) error {
		c := p.Connection
		if c.ID == "" || len(c.ID) > 100 || c.Generation != st.Config.Generation || c.Namespace != st.Config.Scope.Namespace {
			return errors.New("Connector 配置版本或命名空间不匹配，请应用当前部署清单")
		}
		if c.Provider != "kubernetes" && c.Provider != "demo" {
			return errors.New("未知执行端类型")
		}
		if st.Connection.ID != "" && st.Connection.ID != c.ID && time.Since(st.Connection.LastSeen) < 90*time.Second {
			return errors.New("该连接已由另一个 Connector 占用")
		}
		c.LastSeen = time.Now().UTC()
		st.Connection = c
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if (j.Status == "queued" || j.Status == "leased") && time.Now().After(j.ExpiresAt) {
				j.Status = "expired"
			}
			if j.Status == "queued" && j.Automatic && !st.Config.Scope.Automatic {
				j.Status = "interrupted"
			}
			if j.Status == "queued" && j.Generation == c.Generation {
				j.Status = "leased"
				j.Lease = randomID()
				j.Owner = c.ID
				copy := clone(*j)
				reply.Job = &copy
				break
			}
		}
		return nil
	})
	return reply, err
}
func (s *Service) Result(result JobResult) error {
	return s.change(func(st *DiskState) error {
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if j.ID != result.ID {
				continue
			}
			if j.Status != "leased" || j.Owner != result.ConnectorID || j.Lease != result.Lease || time.Now().After(j.ExpiresAt) || j.Generation != st.Config.Generation {
				return errors.New("任务租约无效、已处理或已过期")
			}
			j.Status = "done"
			copy := clone(result)
			j.Result = &copy
			return nil
		}
		return errors.New("任务不存在")
	})
}
func (s *Service) call(ctx context.Context, action string, incident *core.Incident, plan *core.Plan, auto ...bool) (JobResult, error) {
	st := s.current()
	j := Job{ID: randomID()[:24], Action: action, Status: "queued", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(70 * time.Second), Generation: st.Config.Generation, Incident: incident, Plan: plan, PlanHash: core.PlanHash(plan)}
	if len(auto) > 0 {
		j.Automatic = auto[0]
	}
	err := s.change(func(st *DiskState) error {
		if j.Automatic && !st.Config.Scope.Automatic {
			return errors.New("自动修复已暂停")
		}
		if st.Config.Generation != j.Generation || time.Since(st.Connection.LastSeen) > 90*time.Second {
			return errors.New("Connector 离线或授权已改变")
		}
		if len(st.Jobs) >= 100 {
			st.Jobs = st.Jobs[len(st.Jobs)-80:]
		}
		st.Jobs = append(st.Jobs, j)
		return nil
	})
	if err != nil {
		return JobResult{}, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		_ = s.change(func(st *DiskState) error {
			for i := range st.Jobs {
				if st.Jobs[i].ID == j.ID && st.Jobs[i].Status != "done" {
					st.Jobs[i].Status = "interrupted"
				}
			}
			return nil
		})
	}()
	for {
		select {
		case <-ctx.Done():
			return JobResult{}, ctx.Err()
		case <-ticker.C:
			for _, job := range s.current().Jobs {
				if job.ID != j.ID {
					continue
				}
				if job.Result != nil {
					if job.Result.Error != "" {
						return *job.Result, errors.New(job.Result.Error)
					}
					return *job.Result, nil
				}
				if time.Now().After(job.ExpiresAt) || job.Status == "expired" || job.Status == "interrupted" {
					return JobResult{}, errors.New("执行端未在期限内回传结果；不会重新派发写操作，请核对实际状态")
				}
			}
		}
	}
}
func allowed(c Config, t core.Target, action string) bool {
	if t.Namespace != c.Scope.Namespace {
		return false
	}
	targetOK := false
	for _, v := range c.Scope.Targets {
		if v.Name == t.Name && v.Kind == t.Kind {
			targetOK = true
		}
	}
	if !targetOK {
		return false
	}
	if action == "" {
		return true
	}
	for _, a := range c.Scope.Actions {
		if a == action {
			return true
		}
	}
	return false
}
func (s *Service) caseUpdate(id, status, title, detail string, fn func(*Case)) error {
	return s.change(func(st *DiskState) error {
		for i := range st.Cases {
			c := &st.Cases[i]
			if c.Incident.ID == id {
				if fn != nil {
					fn(c)
				}
				c.Incident.Status = status
				c.Incident.LastSeen = time.Now().UTC()
				c.Incident.Timeline = append(c.Incident.Timeline, core.Step{At: time.Now().UTC(), Status: status, Title: title, Detail: detail})
				event(st, status, title+" · "+c.Incident.Target.Name)
				return nil
			}
		}
		return errors.New("故障记录不存在")
	})
}
func (s *Service) getCase(id string) (Case, error) {
	for _, c := range s.current().Cases {
		if c.Incident.ID == id {
			return c, nil
		}
	}
	return Case{}, errors.New("故障记录不存在")
}
func (s *Service) start(work func(context.Context) error) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("已有任务运行中")
	}
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return errors.New("平台正在停止")
	}
	s.running = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(s.ctx, 8*time.Minute)
		defer cancel()
		if err := work(ctx); err != nil {
			_ = s.change(func(st *DiskState) error { st.LastError = err.Error(); event(st, "error", err.Error()); return nil })
		}
	}()
	return nil
}
func (s *Service) Scan() error { return s.start(s.scan) }
func (s *Service) scan(ctx context.Context) error {
	r, err := s.call(ctx, "collect", nil, nil)
	if err != nil {
		return err
	}
	if r.Snapshot == nil {
		return errors.New("执行端未返回巡检快照")
	}
	snapshot := r.Snapshot
	if err = s.change(func(st *DiskState) error {
		st.Snapshot = snapshot
		st.LastScan = time.Now().UTC()
		st.LastError = ""
		for _, f := range snapshot.Findings {
			if !allowed(st.Config, f.Target, "") {
				continue
			}
			exists := false
			for i := range st.Cases {
				c := &st.Cases[i]
				if c.Incident.Key == f.Key && c.Incident.Target.UID == f.Target.UID && c.Incident.Status != "resolved" {
					exists = true
					c.Incident.LastSeen = time.Now().UTC()
					c.Incident.Occurrences++
					if c.Incident.Status == "new" {
						c.Incident.Target = f.Target
						c.Incident.Evidence = f.Evidence
					}
				}
			}
			if exists {
				continue
			}
			if len(st.Cases) >= 100 {
				remove := -1
				for i, c := range st.Cases {
					if c.Incident.Status == "resolved" {
						remove = i
						break
					}
				}
				if remove < 0 {
					return errors.New("未关闭事件已达 100 条，请先处理现有事件")
				}
				st.Cases = append(st.Cases[:remove], st.Cases[remove+1:]...)
			}
			st.Cases = append(st.Cases, Case{Generation: st.Config.Generation, Incident: core.Incident{ID: randomID()[:16], Key: f.Key, Type: f.Type, Title: f.Title, Severity: f.Severity, Status: "new", Target: f.Target, Evidence: f.Evidence, FirstSeen: time.Now().UTC(), LastSeen: time.Now().UTC(), Occurrences: 1, Timeline: []core.Step{{At: time.Now().UTC(), Status: "new", Title: "实时巡检发现异常", Detail: "以 Connector 采集的状态与日志为诊断依据"}}}})
		}
		event(st, "inspection", "实时巡检完成")
		return nil
	}); err != nil {
		return err
	}
	st := s.current()
	// A notification clearing is not recovery proof. For an event stopped before
	// any write, a clean snapshot can trigger a separate read-only verification.
	// Preserve its failure timeline and explicitly record external recovery.
	for _, c := range st.Cases {
		if c.Incident.Status != "manual" || c.Incident.ExecutionCount != 0 || c.Generation != st.Config.Generation || !allowed(st.Config, c.Incident.Target, "") {
			continue
		}
		stillPresent := false
		for _, f := range snapshot.Findings {
			if f.Key == c.Incident.Key && f.Target.UID == c.Incident.Target.UID {
				stillPresent = true
				break
			}
		}
		if stillPresent {
			continue
		}
		verification, e := s.call(ctx, "verify", &c.Incident, nil)
		if e == nil && verification.OK {
			if e = s.caseUpdate(c.Incident.ID, "resolved", "外部恢复验证通过", "本事件未执行平台写操作；Connector 已独立验证对象身份、就绪端点和业务访问。"+verification.Detail, func(c *Case) { now := time.Now().UTC(); c.Incident.ResolvedAt = &now; c.ReviewExpires = time.Time{} }); e != nil {
				return e
			}
		}
		break
	}
	if !st.Config.ModelTested {
		return nil
	}
	for _, c := range st.Cases {
		if c.Incident.Status == "new" && c.Generation == st.Config.Generation {
			if err = s.process(ctx, c.Incident.ID); err != nil {
				_ = s.caseUpdate(c.Incident.ID, "manual", "已停止自动处理", err.Error(), nil)
			}
			break
		}
	}
	return nil
}
func (s *Service) process(ctx context.Context, id string) error {
	c, err := s.getCase(id)
	if err != nil {
		return err
	}
	cfg := s.current().Config
	if !allowed(cfg, c.Incident.Target, "") || c.Generation != cfg.Generation {
		return errors.New("事件目标不在当前授权范围")
	}
	if err = s.caseUpdate(id, "diagnosing", "模型初诊", cfg.Model+" 正在判断现有证据是否足以进入计划阶段", nil); err != nil {
		return err
	}
	m, err := s.factory(cfg)
	if err != nil {
		return err
	}
	first, err := m.Investigate(ctx, c.Incident)
	if err != nil {
		return fmt.Errorf("模型初诊未通过：%w", err)
	}
	if err = core.ValidateTriage(first, c.Incident); err != nil {
		return err
	}
	if err = s.caseUpdate(id, "diagnosing", "初诊判断已记录", first.Assessment.Conclusion, func(c *Case) { c.Investigation = &first }); err != nil {
		return err
	}
	d := *first.Assessment
	if d.Disposition == "inspect" {
		d, err = s.followUp(ctx, id, m, c.Incident, first)
		if err != nil {
			return err
		}
	}
	if err = s.caseUpdate(id, "diagnosing", "诊断完成", d.Conclusion, func(c *Case) { c.Incident.Diagnosis = &d }); err != nil {
		return err
	}
	if d.Disposition != "consider_plan" {
		return errors.New("模型诊断依据不足，停止生成修复计划；请查看诊断建议并补充证据")
	}
	if c.Incident.Type == "RolloutFailure" {
		return errors.New("发布故障诊断已完成；此类事件仅提供核查和处置建议，请核对诊断后人工处理")
	}
	c, _ = s.getCase(id)
	r, err := s.call(ctx, "plan", &c.Incident, nil)
	if err != nil {
		return err
	}
	if r.Plan == nil || !allowed(cfg, r.Plan.Target, r.Plan.Action) || r.Plan.Target != c.Incident.Target || r.Plan.BaselineID == "" {
		return errors.New("未生成符合授权和健康基线要求的计划")
	}
	if err = s.caseUpdate(id, "reviewing", "规则检查通过，正在独立复核", "复核只审查证据和计划，不授予写权限", func(c *Case) { c.Incident.Plan = r.Plan }); err != nil {
		return err
	}
	c, _ = s.getCase(id)
	review, err := m.Review(ctx, c.Incident, *r.Plan)
	if err != nil {
		return err
	}
	if review.PlanHash != core.PlanHash(r.Plan) {
		return errors.New("复核未绑定当前计划")
	}
	status := "planned"
	if review.Decision == "reject" {
		status = "rejected"
	}
	if review.Decision == "manual" {
		status = "manual"
	}
	if err = s.caseUpdate(id, status, "自动复核完成", review.Reason, func(c *Case) { c.Review = &review; c.ReviewExpires = time.Now().UTC().Add(10 * time.Minute) }); err != nil {
		return err
	}
	if review.Decision == "approve" && cfg.Scope.Automatic {
		return s.execute(ctx, id, review.PlanHash, true)
	}
	return nil
}
func (s *Service) execute(ctx context.Context, id, hash string, automatic bool) error {
	c, err := s.getCase(id)
	if err != nil {
		return err
	}
	cfg := s.current().Config
	if c.Incident.Plan == nil || c.Incident.ExecutionCount != 0 || core.PlanHash(c.Incident.Plan) != hash || c.Generation != cfg.Generation || !allowed(cfg, c.Incident.Plan.Target, c.Incident.Plan.Action) {
		return errors.New("授权、计划或执行次数检查未通过")
	}
	if c.Review == nil || c.Review.Decision == "reject" || time.Now().After(c.ReviewExpires) || automatic && (c.Review.Decision != "approve" || !cfg.Scope.Automatic) {
		return errors.New("复核结果不允许执行或已经过期")
	}
	if c.Review.PlanHash != hash {
		return errors.New("已复核计划发生变化")
	}
	r, err := s.call(ctx, "plan", &c.Incident, nil)
	if err != nil {
		return err
	}
	if core.PlanHash(r.Plan) != hash {
		return errors.New("执行前复检发现对象、基线或计划已变化；旧复核作废")
	}
	if err = s.caseUpdate(id, "executing", "执行受控修复", "已重新核对目标与计划；本事件只尝试一次写操作", func(c *Case) { c.Incident.ExecutionCount++ }); err != nil {
		return err
	}
	r, err = s.call(ctx, "execute", &c.Incident, c.Incident.Plan, automatic)
	if err != nil {
		return err
	}
	if !r.OK {
		return errors.New("Connector 未确认写入成功")
	}
	if err = s.caseUpdate(id, "verifying", "变更已提交，验证业务恢复", r.Detail, nil); err != nil {
		return err
	}
	r, err = s.call(ctx, "verify", &c.Incident, nil)
	if err != nil {
		return err
	}
	if !r.OK {
		return errors.New("业务恢复未通过：" + r.Detail)
	}
	return s.caseUpdate(id, "resolved", "业务恢复验证通过", r.Detail, func(c *Case) { now := time.Now().UTC(); c.Incident.ResolvedAt = &now })
}
func (s *Service) Approve(id, hash string) error {
	c, err := s.getCase(id)
	if err != nil {
		return err
	}
	if c.Incident.Plan == nil || core.PlanHash(c.Incident.Plan) != hash {
		return errors.New("请确认当前显示的具体计划")
	}
	return s.start(func(ctx context.Context) error {
		err := s.execute(ctx, id, hash, false)
		if err != nil {
			_ = s.caseUpdate(id, "manual", "执行已停止", err.Error(), nil)
		}
		return err
	})
}
func (s *Service) Retry(id string) error {
	return s.start(func(ctx context.Context) error {
		c, err := s.getCase(id)
		if err != nil {
			return err
		}
		if c.Incident.ExecutionCount > 0 {
			return errors.New("已经尝试过写操作，不自动重试")
		}
		r, err := s.call(ctx, "collect", nil, nil)
		if err != nil {
			return err
		}
		if r.Snapshot == nil {
			return errors.New("缺少最新证据")
		}
		generation := s.current().Config.Generation
		found := false
		for _, f := range r.Snapshot.Findings {
			if f.Key == c.Incident.Key && f.Target.UID == c.Incident.Target.UID {
				found = true
				err = s.caseUpdate(id, "new", "重新采集证据", "原复核已作废", func(c *Case) {
					c.Incident.Target = f.Target
					c.Incident.Evidence = f.Evidence
					c.Incident.Plan = nil
					c.Incident.Diagnosis = nil
					c.Investigation = nil
					c.Review = nil
					c.ReviewExpires = time.Time{}
					c.Generation = generation
				})
				break
			}
		}
		if err != nil {
			return err
		}
		if !found {
			return errors.New("最新巡检未复现该故障，请核对当前状态")
		}
		err = s.process(ctx, id)
		if err != nil {
			_ = s.caseUpdate(id, "manual", "已停止自动处理", err.Error(), nil)
		}
		return err
	})
}
func (s *Service) Run() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				st := s.current()
				if st.Config.Generation > 0 && time.Since(st.Connection.LastSeen) < 90*time.Second && time.Since(st.LastScan) >= 30*time.Second && !s.busy() {
					_ = s.Scan()
				}
			}
		}
	}()
}
