package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

var ErrBusy = errors.New("任务正在运行，请等待完成")
var ErrNotFound = errors.New("记录不存在")

type Engine struct {
	mu        sync.RWMutex
	state     State
	store     Store
	provider  Provider
	diagnoser Diagnoser
	busy      map[string]bool
}

func NewEngine(store Store, provider Provider, diagnoser Diagnoser, settings Settings) (*Engine, error) {
	s, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		s = InitialState(settings)
	} else if err != nil {
		return nil, fmt.Errorf("读取状态失败: %w", err)
	}
	if s.Settings.Mode != settings.Mode {
		return nil, errors.New("状态文件的环境模式不匹配，请为不同模式配置独立数据文件")
	}
	s.Settings = settings
	if s.AgentRuns == nil {
		s.AgentRuns = []AgentRun{}
	}
	for i := range s.AgentRuns {
		r := &s.AgentRuns[i]
		if !r.Terminal() {
			r.Status, r.Summary, r.FinishedAt = "interrupted", "服务重启，上次任务已停止；请核对实际状态后新建任务，不自动重放操作。", time.Now().UTC()
			for j := range r.Steps {
				if r.Steps[j].Status == "running" {
					r.Steps[j].Status = "interrupted"
				}
			}
		}
	}
	for i := range s.Incidents {
		incident := &s.Incidents[i]
		if incident.Status == "executing" || incident.Status == "verifying" || incident.Status == "diagnosing" {
			incident.Status = "manual"
			incident.Plan = nil
			incident.Timeline = append(incident.Timeline, Step{At: time.Now().UTC(), Status: "manual", Title: "服务重启后待核验", Detail: "上一次任务未正常结束。不会自动重放写操作，请重新巡检并核对实际状态。"})
		}
	}
	if err := store.Save(s); err != nil {
		return nil, err
	}
	return &Engine{state: s, store: store, provider: provider, diagnoser: diagnoser, busy: map[string]bool{}}, nil
}

func id(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}
func (e *Engine) State() State { e.mu.RLock(); defer e.mu.RUnlock(); return clone(e.state) }
func (e *Engine) mutate(fn func(*State) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.state)
	if err := fn(&next); err != nil {
		return err
	}
	next.Revision++
	if len(next.Audits) > 2000 {
		next.Audits = next.Audits[len(next.Audits)-2000:]
	}
	if len(next.Inspections) > 500 {
		next.Inspections = next.Inspections[len(next.Inspections)-500:]
	}
	if err := e.store.Save(next); err != nil {
		return fmt.Errorf("保存状态失败，操作未提交: %w", err)
	}
	e.state = next
	return nil
}
func (e *Engine) acquire(key string) (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy[key] || e.busy["environment"] || (key == "environment" && len(e.busy) > 0) {
		return nil, ErrBusy
	}
	e.busy[key] = true
	return func() { e.mu.Lock(); delete(e.busy, key); e.mu.Unlock() }, nil
}
func incidentByID(state *State, incidentID string) (*Incident, error) {
	for i := range state.Incidents {
		if state.Incidents[i].ID == incidentID {
			return &state.Incidents[i], nil
		}
	}
	return nil, ErrNotFound
}
func step(i *Incident, status, title, detail string) {
	i.Status = status
	i.Timeline = append(i.Timeline, Step{At: time.Now().UTC(), Status: status, Title: title, Detail: detail})
}
func audit(s *State, actor, action, target, result, detail string) {
	s.Audits = append(s.Audits, Audit{ID: id("AUD"), At: time.Now().UTC(), Actor: actor, Action: action, Target: target, Result: result, Detail: detail})
}

func (e *Engine) Inspect(ctx context.Context, trigger string) error {
	release, err := e.acquire("inspection")
	if err != nil {
		return err
	}
	defer release()
	start := time.Now().UTC()
	snapshot, collectErr := e.provider.Collect(ctx)
	persistErr := e.mutate(func(s *State) error {
		run := Inspection{ID: id("CHK"), Trigger: trigger, StartedAt: start, DurationMS: time.Since(start).Milliseconds(), Status: "completed", Findings: len(snapshot.Findings)}
		if collectErr != nil {
			run.Status = "failed"
			run.Error = collectErr.Error()
			audit(s, trigger, "inspection", s.Settings.Namespace, "failed", run.Error)
		} else {
			s.Snapshot = &snapshot
			for _, finding := range snapshot.Findings {
				var existing *Incident
				for i := range s.Incidents {
					if s.Incidents[i].Key == finding.Key && s.Incidents[i].Target.UID == finding.Target.UID && s.Incidents[i].Status != "resolved" {
						existing = &s.Incidents[i]
						break
					}
				}
				if existing != nil {
					existing.LastSeen = snapshot.At
					existing.Occurrences++
					if existing.Status != "executing" && existing.Status != "verifying" && existing.Status != "diagnosing" {
						if existing.Target.Version != finding.Target.Version {
							existing.Plan = nil
							step(existing, "new", "对象状态已更新", "先前计划已失效，需重新诊断。")
						}
						existing.Evidence = finding.Evidence
						existing.Target = finding.Target
					}
				} else {
					i := Incident{ID: id("INC"), Key: finding.Key, Type: finding.Type, Title: finding.Title, Severity: finding.Severity, Target: finding.Target, FirstSeen: snapshot.At, LastSeen: snapshot.At, Occurrences: 1, Evidence: finding.Evidence, Timeline: []Step{}}
					step(&i, "new", "发现异常", "已归集对象状态和事件证据，等待诊断。")
					s.Incidents = append(s.Incidents, i)
					for j := range s.Experiments {
						x := &s.Experiments[j]
						if x.Fault == i.Type && x.Status == "injected" {
							ms := snapshot.At.Sub(x.At).Milliseconds()
							x.DetectionMS = &ms
							x.IncidentID = i.ID
							x.Status = "detected"
						}
					}
				}
			}
			audit(s, trigger, "inspection", s.Settings.Namespace, "success", fmt.Sprintf("检查 %d 个 Pod，发现 %d 项异常", snapshot.Pods, len(snapshot.Findings)))
		}
		s.Inspections = append(s.Inspections, run)
		return nil
	})
	if persistErr != nil {
		return persistErr
	}
	return collectErr
}

func (e *Engine) Diagnose(ctx context.Context, incidentID string) error {
	return e.diagnose(ctx, incidentID, nil, nil)
}

func (e *Engine) diagnose(ctx context.Context, incidentID string, supplied *Diagnosis, expected *Target) error {
	release, err := e.acquire(incidentID)
	if err != nil {
		return err
	}
	defer release()
	s := e.State()
	incident, err := incidentByID(&s, incidentID)
	if err != nil {
		return err
	}
	if incident.Status == "resolved" {
		return errors.New("事件已恢复")
	}
	if expected != nil && (incident.Target.UID != expected.UID || incident.Target.Version != expected.Version) {
		return errors.New("分析期间目标身份或版本已变化，请重新采集证据")
	}
	if err := e.mutate(func(s *State) error {
		i, _ := incidentByID(s, incidentID)
		step(i, "diagnosing", "开始诊断", "关联证据并检索对应排障条目。模型建议不直接授权写操作。")
		return nil
	}); err != nil {
		return err
	}
	diagnosis := RuleDiagnosis(*incident)
	if supplied != nil {
		diagnosis = *supplied
	} else if e.diagnoser != nil {
		d, err := e.diagnoser.Diagnose(ctx, *incident)
		if err == nil {
			diagnosis = d
		} else {
			diagnosis.Limitations += " 模型调用失败，已降级为规则诊断：" + err.Error()
		}
	}
	plan, planErr := e.provider.Plan(ctx, *incident)
	return e.mutate(func(s *State) error {
		i, _ := incidentByID(s, incidentID)
		if i.Target.Version != incident.Target.Version {
			step(i, "new", "诊断过期", "对象已更新，请重新巡检诊断。")
			return nil
		}
		i.Diagnosis = &diagnosis
		i.Plan = plan
		if planErr != nil || plan == nil {
			reason := "该场景需要人工处理。"
			if planErr != nil {
				reason = planErr.Error()
			}
			step(i, "manual", "等待人工处理", reason)
		} else {
			step(i, "planned", "生成处置计划", plan.Summary)
		}
		audit(s, "operator", "diagnose", incidentID, "success", "诊断引擎："+diagnosis.Engine)
		return nil
	})
}

func (e *Engine) Execute(ctx context.Context, incidentID string, automatic bool) error {
	return e.executePlan(ctx, incidentID, automatic, "")
}

func (e *Engine) executePlan(ctx context.Context, incidentID string, automatic bool, expectedHash string) error {
	s := e.State()
	incident, err := incidentByID(&s, incidentID)
	if err != nil {
		return err
	}
	release, err := e.acquire(incidentID)
	if err != nil {
		return err
	}
	defer release()
	// Re-read after acquiring the incident lock; approval binds the complete plan.
	s = e.State()
	incident, err = incidentByID(&s, incidentID)
	if err != nil {
		return err
	}
	if expectedHash != "" && PlanHash(incident.Plan) != expectedHash {
		return errors.New("已确认的处置计划发生变化，请重新分析并确认")
	}
	workloadLock, err := e.acquire(incident.Target.Namespace + "/" + incident.Target.Kind + "/" + incident.Target.Name)
	if err != nil {
		return err
	}
	defer workloadLock()
	if !s.Settings.WritesEnabled {
		return errors.New("当前环境为只读模式，未启用写操作")
	}
	if incident.Status != "planned" || incident.Plan == nil {
		return errors.New("请先生成有效处置计划")
	}
	if incident.ExecutionCount >= 1 {
		return errors.New("本事件已执行过一次，不会自动重试写操作")
	}
	allowed := false
	for _, p := range s.Policies {
		if p.Action == incident.Plan.Action && p.Enabled && (!automatic || p.Automatic) {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("处置策略未允许此操作")
	}
	newPlan, err := e.provider.Plan(ctx, *incident)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(newPlan)
	b, _ := json.Marshal(incident.Plan)
	if string(a) != string(b) {
		return errors.New("对象或基线已变化，计划已过期，请重新巡检诊断")
	}
	actor := "operator"
	if automatic {
		actor = "scheduler"
	}
	// Persist intent before any external write; failure here prevents execution.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.mutate(func(s *State) error {
		i, _ := incidentByID(s, incidentID)
		if i.Status != "planned" || i.ExecutionCount > 0 {
			return ErrBusy
		}
		currentAllowed := false
		for _, p := range s.Policies {
			if p.Action == i.Plan.Action && p.Enabled && (!automatic || p.Automatic) {
				currentAllowed = true
			}
		}
		if !currentAllowed {
			return errors.New("策略已更新，不再允许执行")
		}
		i.ExecutionCount++
		step(i, "executing", "执行受控修复", "校验对象身份与版本，提交服务端预检，再执行限定字段变更。")
		audit(s, actor, i.Plan.Action, incidentID, "started", i.Plan.Summary)
		return nil
	}); err != nil {
		return err
	}
	detail, executeErr := e.provider.Execute(ctx, *incident.Plan)
	if executeErr != nil {
		persistErr := e.mutate(func(s *State) error {
			i, _ := incidentByID(s, incidentID)
			step(i, "manual", "执行未完成", executeErr.Error())
			audit(s, actor, "execute", incidentID, "failed", executeErr.Error())
			return nil
		})
		if persistErr != nil {
			return errors.Join(executeErr, persistErr)
		}
		return executeErr
	}
	if err := e.mutate(func(s *State) error {
		i, _ := incidentByID(s, incidentID)
		step(i, "verifying", "变更已提交，验证恢复", detail)
		audit(s, actor, "execute", incidentID, "submitted", detail)
		return nil
	}); err != nil {
		return err
	}
	return e.verify(ctx, incidentID)
}

func (e *Engine) Verify(ctx context.Context, incidentID string) error {
	release, err := e.acquire(incidentID)
	if err != nil {
		return err
	}
	defer release()
	return e.verify(ctx, incidentID)
}
func (e *Engine) verify(ctx context.Context, incidentID string) error {
	s := e.State()
	incident, err := incidentByID(&s, incidentID)
	if err != nil {
		return err
	}
	if incident.Status == "resolved" {
		return nil
	}
	if err := e.mutate(func(s *State) error {
		i, _ := incidentByID(s, incidentID)
		step(i, "verifying", "开始恢复验证", "同时检查对象状态、服务可用性及稳定窗口。")
		return nil
	}); err != nil {
		return err
	}
	ok, detail, verifyErr := e.provider.Verify(ctx, *incident)
	return e.mutate(func(s *State) error {
		i, _ := incidentByID(s, incidentID)
		if verifyErr != nil {
			detail = verifyErr.Error()
		}
		if ok && verifyErr == nil {
			now := time.Now().UTC()
			i.ResolvedAt = &now
			step(i, "resolved", "恢复验证通过", detail)
			for j := range s.Experiments {
				x := &s.Experiments[j]
				if x.IncidentID == i.ID && x.RecoveryMS == nil {
					ms := now.Sub(x.At).Milliseconds()
					x.RecoveryMS = &ms
					x.Status = "recovered"
				}
			}
			audit(s, "verifier", "verify", incidentID, "success", detail)
		} else {
			step(i, "manual", "尚未确认恢复", detail)
			audit(s, "verifier", "verify", incidentID, "unconfirmed", detail)
		}
		return nil
	})
}

func (e *Engine) SetPolicy(action string, enabled, automatic bool) error {
	return e.mutate(func(s *State) error {
		if automatic && (!enabled || !s.Settings.WritesEnabled) {
			return errors.New("自动处置需要启用该策略及写操作")
		}
		for i := range s.Policies {
			p := &s.Policies[i]
			if p.Action == action {
				p.Enabled = enabled
				p.Automatic = automatic
				audit(s, "operator", "policy.update", action, "success", fmt.Sprintf("enabled=%t, automatic=%t", enabled, automatic))
				return nil
			}
		}
		return ErrNotFound
	})
}

func (e *Engine) Tick(ctx context.Context) error {
	if err := e.Inspect(ctx, "scheduler"); err != nil {
		return err
	}
	s := e.State()
	if !s.Settings.WritesEnabled {
		return nil
	}
	for _, i := range s.Incidents {
		if i.Status != "new" || i.ExecutionCount > 0 {
			continue
		}
		auto := false
		for _, book := range Runbooks {
			if book.Type == i.Type {
				for _, p := range s.Policies {
					if p.Action == book.Action && p.Enabled && p.Automatic {
						auto = true
					}
				}
			}
		}
		if !auto {
			continue
		}
		if err := e.Diagnose(ctx, i.ID); err != nil {
			continue
		}
		_ = e.Execute(ctx, i.ID, true)
	}
	return nil
}

func (e *Engine) Inject(fault string) error {
	control, ok := e.provider.(DemoControl)
	if !ok {
		return errors.New("在线故障注入仅开放给演示环境；真实集群请使用实验脚本")
	}
	release, err := e.acquire("inspection")
	if err != nil {
		return err
	}
	defer release()
	if err := control.Inject(fault); err != nil {
		return err
	}
	return e.mutate(func(s *State) error {
		s.Experiments = append(s.Experiments, Experiment{ID: id("EXP"), At: time.Now().UTC(), Fault: fault, Status: "injected", Mode: "demo"})
		audit(s, "operator", "demo.inject", fault, "success", "仅修改独立演示环境")
		return nil
	})
}

func (e *Engine) ResetDemo() error {
	control, ok := e.provider.(DemoControl)
	if !ok {
		return errors.New("当前不是演示环境")
	}
	release, err := e.acquire("environment")
	if err != nil {
		return err
	}
	defer release()
	if err := control.Reset(); err != nil {
		return err
	}
	return e.mutate(func(s *State) error {
		for i := range s.Incidents {
			if s.Incidents[i].Status != "resolved" {
				step(&s.Incidents[i], "manual", "演示环境已复位", "故障已由实验复位清除，可点击验证恢复；不计为自动修复成功。")
			}
		}
		for i := range s.Experiments {
			if s.Experiments[i].RecoveryMS == nil {
				s.Experiments[i].Status = "reset"
				s.Experiments[i].IncidentID = ""
			}
		}
		audit(s, "operator", "demo.reset", "demo", "success", "保留历史记录，重置演示资源为健康状态")
		return nil
	})
}

func (e *Engine) CaptureBaseline(ctx context.Context, kind, name string, capture func(context.Context, string, string) error) error {
	release, err := e.acquire("environment")
	if err != nil {
		return err
	}
	defer release()
	if err := e.mutate(func(s *State) error {
		audit(s, "operator", "baseline.capture", kind+"/"+name, "started", "验证资源就绪与业务探测后记录健康基线")
		return nil
	}); err != nil {
		return err
	}
	captureErr := capture(ctx, kind, name)
	persistErr := e.mutate(func(s *State) error {
		result, detail := "success", "健康基线已保存"
		if captureErr != nil {
			result = "failed"
			detail = captureErr.Error()
		}
		audit(s, "operator", "baseline.capture", kind+"/"+name, result, detail)
		return nil
	})
	return errors.Join(captureErr, persistErr)
}

func (e *Engine) RecordRejected(path string, cause error) error {
	if len(path) > 200 {
		path = path[:200]
	}
	return e.mutate(func(s *State) error {
		audit(s, "operator", "request.rejected", path, "rejected", cause.Error())
		return nil
	})
}
