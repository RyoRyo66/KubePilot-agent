package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

type AgentRunner struct {
	engine  *Engine
	model   AgentModel
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	wg      sync.WaitGroup
	timeout time.Duration
}

func NewAgentRunner(parent context.Context, engine *Engine, model AgentModel, timeout time.Duration) *AgentRunner {
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	return &AgentRunner{engine: engine, model: model, ctx: ctx, cancel: cancel, active: map[string]context.CancelFunc{}, timeout: timeout}
}
func (a *AgentRunner) Close() { a.mu.Lock(); a.cancel(); a.mu.Unlock(); a.wg.Wait() }
func (a *AgentRunner) Get(runID string) (AgentRun, error) {
	for _, run := range a.engine.State().AgentRuns {
		if run.ID == runID {
			return run, nil
		}
	}
	return AgentRun{}, ErrNotFound
}
func (a *AgentRunner) update(runID string, fn func(*AgentRun) error) error {
	return a.engine.mutate(func(s *State) error {
		for i := range s.AgentRuns {
			if s.AgentRuns[i].ID == runID {
				return fn(&s.AgentRuns[i])
			}
		}
		return ErrNotFound
	})
}

var workloadPattern = regexp.MustCompile("^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$")

func (a *AgentRunner) Start(request AgentRequest, trigger string) (AgentRun, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ctx.Err(); err != nil {
		return AgentRun{}, err
	}
	s := a.engine.State()
	if request.Namespace != s.Settings.Namespace || len(request.Workload) > 253 || !workloadPattern.MatchString(request.Workload) {
		return AgentRun{}, errors.New("请填写当前命名空间内有效的工作负载名称")
	}
	if request.Mode != "observe" && request.Mode != "approval" && request.Mode != "automatic" {
		return AgentRun{}, errors.New("任务模式仅支持 observe、approval、automatic")
	}
	if len(request.Goal) > 2000 {
		return AgentRun{}, errors.New("任务描述过长")
	}
	if request.MaxSteps == 0 {
		request.MaxSteps = 8
	}
	if request.MaxSteps < 3 || request.MaxSteps > 16 {
		return AgentRun{}, errors.New("工具步数必须在 3 到 16 之间")
	}
	if request.IncidentID != "" {
		i, err := incidentByID(&s, request.IncidentID)
		if err != nil || i.Target.Name != request.Workload || i.Target.Namespace != request.Namespace {
			return AgentRun{}, errors.New("事件不在任务目标范围内")
		}
	}
	if len(a.active) > 0 {
		return AgentRun{}, ErrBusy
	}
	for _, r := range s.AgentRuns {
		if !r.Terminal() && r.Request.Workload == request.Workload && r.Request.Namespace == request.Namespace {
			return AgentRun{}, errors.New("该目标已有运行中或等待确认的任务")
		}
	}
	name := "rules"
	if a.model != nil {
		name = a.model.Name()
	}
	run := AgentRun{ID: id("RUN"), Request: request, Status: "running", Trigger: trigger, Engine: name, PromptHash: AgentPromptHash(), StartedAt: time.Now().UTC(), Summary: "正在采集最新集群状态", Steps: []AgentStep{}}
	if err := a.engine.mutate(func(s *State) error {
		if len(s.AgentRuns) >= 100 {
			remove := -1
			for i := range s.AgentRuns {
				if s.AgentRuns[i].Terminal() {
					remove = i
					break
				}
			}
			if remove < 0 {
				return errors.New("未结束任务过多，请先取消旧任务")
			}
			s.AgentRuns = append(s.AgentRuns[:remove], s.AgentRuns[remove+1:]...)
		}
		s.AgentRuns = append(s.AgentRuns, run)
		audit(s, trigger, "agent.start", run.ID, "started", request.Namespace+"/"+request.Workload+" mode="+request.Mode)
		return nil
	}); err != nil {
		return AgentRun{}, err
	}
	a.launch(run.ID, func(ctx context.Context) error { return a.analyze(ctx, run.ID) })
	return run, nil
}

// Called with a.mu held. Workers outlive the HTTP request, not the service.
func (a *AgentRunner) launch(runID string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(a.ctx, a.timeout)
	a.active[runID] = cancel
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { cancel(); a.mu.Lock(); delete(a.active, runID); a.mu.Unlock() }()
		defer func() {
			if recover() != nil {
				slog.Error("agent worker panic", "run", runID)
				a.finish(runID, "failed", "任务内部异常，已停止后续步骤")
			}
		}()
		err := fn(ctx)
		if ctx.Err() != nil {
			status, detail := "cancelled", "任务已停止；已提交的变更不会撤销，请核对关联事件的实际状态。"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status, detail = "needs_attention", "任务达到时间上限，已停止后续步骤，请核对实际状态。"
			}
			a.finish(runID, status, detail)
		} else if err != nil {
			a.finish(runID, "failed", err.Error())
		}
	}()
}
func (a *AgentRunner) finish(runID, status, summary string) {
	err := a.engine.mutate(func(s *State) error {
		for i := range s.AgentRuns {
			r := &s.AgentRuns[i]
			if r.ID != runID {
				continue
			}
			if r.Terminal() {
				return nil
			}
			r.Status, r.Summary, r.FinishedAt = status, summary, time.Now().UTC()
			for j := range r.Steps {
				if r.Steps[j].Status == "running" {
					r.Steps[j].Status, r.Steps[j].FinishedAt = "interrupted", r.FinishedAt
					r.Steps[j].DurationMS = r.FinishedAt.Sub(r.Steps[j].StartedAt).Milliseconds()
				}
			}
			audit(s, "agent", "agent.finish", runID, status, summary)
			return nil
		}
		return ErrNotFound
	})
	if err != nil {
		slog.Error("agent result not persisted", "run", runID, "error", err)
	}
}
func (a *AgentRunner) tool(ctx context.Context, runID, name string, arguments any, fn func() (any, error)) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	entry := AgentStep{ID: id("STEP"), Tool: name, Status: "running", StartedAt: time.Now().UTC(), Arguments: args}
	if err := a.update(runID, func(r *AgentRun) error { r.Steps = append(r.Steps, entry); return nil }); err != nil {
		return nil, err
	}
	value, callErr := fn()
	output, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(output) > 96*1024 {
		output = json.RawMessage(`{"notice":"结果过大，请查看关联事件详情"}`)
	}
	persistErr := a.update(runID, func(r *AgentRun) error {
		for i := range r.Steps {
			s := &r.Steps[i]
			if s.ID != entry.ID {
				continue
			}
			s.Status, s.FinishedAt, s.DurationMS, s.Output = "completed", time.Now().UTC(), time.Since(entry.StartedAt).Milliseconds(), output
			if callErr != nil {
				s.Status, s.Error = "failed", callErr.Error()
			}
		}
		return nil
	})
	return output, errors.Join(callErr, persistErr)
}
func (a *AgentRunner) Cancel(runID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, err := a.Get(runID)
	if err != nil {
		return err
	}
	if run.Terminal() {
		return errors.New("任务已结束")
	}
	if cancel := a.active[runID]; cancel != nil {
		if err := a.update(runID, func(r *AgentRun) error {
			if !r.Terminal() {
				r.Status, r.Summary = "cancelling", "正在停止后续步骤"
			}
			return nil
		}); err != nil {
			return err
		}
		cancel()
	} else {
		a.finish(runID, "cancelled", "待确认任务已取消，未通过本任务提交修复。")
	}
	return nil
}
func (a *AgentRunner) Approve(runID, hash string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return a.ctx.Err()
	}
	run, err := a.Get(runID)
	if err != nil {
		return err
	}
	if run.Status != "awaiting_approval" || hash == "" || hash != run.PlanHash {
		return errors.New("任务状态或计划摘要不匹配，请重新查看任务")
	}
	if time.Now().After(run.ApprovalDue) {
		a.finish(runID, "needs_attention", "处置确认已过期，请新建任务重新采集和分析")
		return errors.New("确认已过期")
	}
	if len(a.active) > 0 {
		return ErrBusy
	}
	if err := a.engine.mutate(func(s *State) error {
		for i := range s.AgentRuns {
			r := &s.AgentRuns[i]
			if r.ID != runID {
				continue
			}
			r.ApprovedAt, r.Status, r.Summary = time.Now().UTC(), "running", "已确认此版本计划，准备重新校验并执行"
			audit(s, "operator", "agent.approve", runID, "approved", "planHash="+hash)
			return nil
		}
		return ErrNotFound
	}); err != nil {
		return err
	}
	a.launch(runID, func(ctx context.Context) error { return a.execute(ctx, runID, false) })
	return nil
}
func (a *AgentRunner) currentIncident(incidentID string) Incident {
	s := a.engine.State()
	i, err := incidentByID(&s, incidentID)
	if err != nil {
		return Incident{}
	}
	return *i
}
func (a *AgentRunner) analyze(ctx context.Context, runID string) error {
	run, _ := a.Get(runID)
	if _, err := a.tool(ctx, runID, "inspect_cluster", map[string]string{"namespace": run.Request.Namespace}, func() (any, error) {
		err := a.engine.Inspect(ctx, "agent")
		s := a.engine.State()
		if err != nil {
			return nil, err
		}
		return map[string]any{"at": s.Snapshot.At, "pods": s.Snapshot.Pods, "readyPods": s.Snapshot.ReadyPods, "warnings": s.Snapshot.Warnings, "findings": len(s.Snapshot.Findings)}, nil
	}); err != nil {
		return err
	}
	s := a.engine.State()
	var incident *Incident
	for j := len(s.Incidents) - 1; j >= 0; j-- {
		i := &s.Incidents[j]
		if i.Target.Namespace == run.Request.Namespace && i.Target.Name == run.Request.Workload && i.Status != "resolved" && (run.Request.IncidentID == "" || i.ID == run.Request.IncidentID) {
			incident = i
			break
		}
	}
	if incident == nil {
		found := false
		for _, r := range s.Snapshot.Resources {
			if r.Namespace == run.Request.Namespace && r.Name == run.Request.Workload {
				found = true
			}
		}
		if !found {
			return errors.New("采集范围内未找到目标工作负载")
		}
		a.finish(runID, "completed", "本次巡检未发现匹配的待处理事件；未提交变更，这不等同于完整业务健康验证。")
		return nil
	}
	if err := a.update(runID, func(r *AgentRun) error { r.IncidentID = incident.ID; return nil }); err != nil {
		return err
	}
	present := false
	for _, f := range s.Snapshot.Findings {
		if f.Key == incident.Key && f.Target.UID == incident.Target.UID {
			present = true
		}
	}
	if !present {
		if _, err := a.tool(ctx, runID, "verify_recovery", map[string]string{"incidentId": incident.ID}, func() (any, error) {
			err := a.engine.Verify(ctx, incident.ID)
			return a.currentIncident(incident.ID), err
		}); err != nil {
			return err
		}
		a.completeFromIncident(runID, incident.ID)
		return nil
	}
	diagnosis, err := a.investigate(ctx, run, *incident)
	if err != nil {
		return err
	}
	if _, err := a.tool(ctx, runID, "build_repair_plan", map[string]string{"incidentId": incident.ID}, func() (any, error) {
		err := a.engine.diagnose(ctx, incident.ID, &diagnosis, &incident.Target)
		return a.currentIncident(incident.ID), err
	}); err != nil {
		return err
	}
	updated := a.currentIncident(incident.ID)
	if err := a.update(runID, func(r *AgentRun) error {
		r.Diagnosis, r.Plan, r.PlanHash = updated.Diagnosis, updated.Plan, PlanHash(updated.Plan)
		return nil
	}); err != nil {
		return err
	}
	if updated.Plan == nil || updated.Status != "planned" {
		a.finish(runID, "needs_attention", "分析已完成，但没有满足条件的受控修复计划；请查看诊断和事件时间线。")
		return nil
	}
	if run.Request.Mode == "observe" {
		a.finish(runID, "completed", "只读分析完成，已生成处置建议；没有执行集群变更。")
		return nil
	}
	run, _ = a.Get(runID)
	automatic := run.Request.Mode == "automatic" && !run.Fallback && s.Settings.WritesEnabled
	allowed := false
	for _, p := range a.engine.State().Policies {
		if p.Action == updated.Plan.Action && p.Enabled && p.Automatic {
			allowed = true
		}
	}
	if automatic && allowed {
		return a.execute(ctx, runID, true)
	}
	return a.update(runID, func(r *AgentRun) error {
		r.Status, r.ApprovalDue = "awaiting_approval", time.Now().UTC().Add(15*time.Minute)
		r.Summary = "分析完成，请核对目标、变更内容与计划版本后确认执行。"
		if r.Fallback {
			r.Summary = "模型不可用或输出未通过校验，已降级规则分析；自动写入已停止，需人工核对计划。"
		}
		if !s.Settings.WritesEnabled {
			r.Summary = "分析完成，当前环境只读；可查看计划，但不能执行。"
		}
		return nil
	})
}
func (a *AgentRunner) execute(ctx context.Context, runID string, automatic bool) error {
	run, _ := a.Get(runID)
	if _, err := a.tool(ctx, runID, "execute_and_verify", map[string]any{"incidentId": run.IncidentID, "planHash": run.PlanHash, "automatic": automatic}, func() (any, error) {
		err := a.engine.executePlan(ctx, run.IncidentID, automatic, run.PlanHash)
		return a.currentIncident(run.IncidentID), err
	}); err != nil {
		return err
	}
	if _, err := a.tool(ctx, runID, "refresh_after_repair", map[string]string{"namespace": run.Request.Namespace}, func() (any, error) {
		err := a.engine.Inspect(ctx, "agent")
		return a.engine.State().Snapshot, err
	}); err != nil {
		return err
	}
	a.completeFromIncident(runID, run.IncidentID)
	return nil
}
func (a *AgentRunner) completeFromIncident(runID, incidentID string) {
	if a.currentIncident(incidentID).Status == "resolved" {
		a.finish(runID, "recovered", "关联事件通过恢复验证。其他事件的状态请查看集群概览。")
	} else {
		a.finish(runID, "needs_attention", "尚未确认业务恢复，已停止后续自动动作，请人工核查。")
	}
}
func (a *AgentRunner) Tick(ctx context.Context) error {
	if err := a.engine.Inspect(ctx, "scheduler"); err != nil {
		return err
	}
	s := a.engine.State()
	if !s.Settings.WritesEnabled {
		return nil
	}
	for _, i := range s.Incidents {
		if i.Status != "new" || i.ExecutionCount > 0 {
			continue
		}
		coolingDown := false
		for _, run := range s.AgentRuns {
			if !run.Terminal() && run.Request.Namespace == i.Target.Namespace && run.Request.Workload == i.Target.Name {
				coolingDown = true
				break
			}
			if run.Trigger == "scheduler" && (run.IncidentID == i.ID || run.Request.IncidentID == i.ID) && time.Since(run.StartedAt) < 5*time.Minute {
				coolingDown = true
				break
			}
		}
		if coolingDown {
			continue
		}
		for _, b := range Runbooks {
			if b.Type != i.Type || b.Action == "" {
				continue
			}
			for _, p := range s.Policies {
				if p.Action == b.Action && p.Enabled && p.Automatic {
					_, err := a.Start(AgentRequest{Namespace: i.Target.Namespace, Workload: i.Target.Name, IncidentID: i.ID, Mode: "automatic", Goal: "定时巡检发现异常，按已授权策略排障并验证"}, "scheduler")
					if errors.Is(err, ErrBusy) {
						return nil
					}
					return err
				}
			}
		}
	}
	return nil
}
