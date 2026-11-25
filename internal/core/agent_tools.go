package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (a *AgentRunner) investigate(ctx context.Context, run AgentRun, incident Incident) (Diagnosis, error) {
	exchanges := []ToolExchange{}
	readEvidence := []Evidence{}
	searched := false
	invalid := 0
	if a.model != nil {
		for n := 0; n < run.Request.MaxSteps; n++ {
			if err := ctx.Err(); err != nil {
				return Diagnosis{}, err
			}
			var decision AgentDecision
			_, err := a.tool(ctx, run.ID, "model_decision", map[string]any{"model": a.model.Name(), "round": n + 1}, func() (any, error) {
				var err error
				decision, err = a.model.Next(ctx, AgentInput{Request: run.Request, Incident: incident, Exchanges: exchanges})
				return map[string]any{"tool": decision.Tool, "inputTokens": decision.InputTokens, "outputTokens": decision.OutputTokens}, err
			})
			if persistErr := a.update(run.ID, func(r *AgentRun) error {
				r.ModelCalls++
				r.InputTokens += decision.InputTokens
				r.OutputTokens += decision.OutputTokens
				return nil
			}); persistErr != nil {
				return Diagnosis{}, persistErr
			}
			if err != nil {
				if ctx.Err() != nil {
					return Diagnosis{}, ctx.Err()
				}
				break
			}
			output, err := a.tool(ctx, run.ID, decision.Tool, decision.Arguments, func() (any, error) {
				switch decision.Tool {
				case "read_evidence":
					var args struct {
						Source string `json:"source"`
					}
					if err := StrictJSON(decision.Arguments, &args); err != nil {
						return nil, err
					}
					if args.Source != "all" && args.Source != "events" && args.Source != "logs" && args.Source != "status" {
						return nil, errors.New("来源只支持 all、events、logs、status")
					}
					items := []Evidence{}
					for _, e := range incident.Evidence {
						source := strings.ToLower(e.Source)
						if args.Source == "all" || args.Source == "events" && strings.Contains(source, "event") || args.Source == "logs" && strings.Contains(source, "log") || args.Source == "status" && (strings.Contains(source, "status") || strings.Contains(source, "pod")) {
							items = append(items, e)
							known := false
							for _, existing := range readEvidence {
								if existing.ID == e.ID {
									known = true
								}
							}
							if !known {
								readEvidence = append(readEvidence, e)
							}
						}
					}
					return items, nil
				case "search_runbooks":
					var args struct {
						Query string `json:"query"`
					}
					if err := StrictJSON(decision.Arguments, &args); err != nil {
						return nil, err
					}
					if strings.TrimSpace(args.Query) == "" || len(args.Query) > 300 {
						return nil, errors.New("检索词不能为空或超过 300 字节")
					}
					hits := SearchRunbooks(args.Query, incident.Type)
					searched = len(hits) > 0
					return hits, nil
				case "submit_diagnosis":
					var d Diagnosis
					if err := StrictJSON(decision.Arguments, &d); err != nil {
						return nil, err
					}
					if !searched {
						return nil, errors.New("请先检索对应排障知识")
					}
					if err := ValidateDiagnosis(d, readEvidence); err != nil {
						return nil, err
					}
					return d, nil
				default:
					return nil, errors.New("该工具不在只读诊断工具列表中")
				}
			})
			if err != nil {
				invalid++
				output, _ = json.Marshal(map[string]string{"error": err.Error()})
			}
			exchanges = append(exchanges, ToolExchange{Decision: decision, Output: output})
			if err == nil && decision.Tool == "submit_diagnosis" {
				var d Diagnosis
				_ = json.Unmarshal(output, &d)
				d.Engine, d.At = a.model.Name(), time.Now().UTC()
				d.Limitations += " 模型分析需结合证据复核；写操作由独立计划和策略授权。"
				return d, nil
			}
			if invalid >= 2 {
				break
			}
		}
		if err := a.update(run.ID, func(r *AgentRun) error { r.Fallback = true; return nil }); err != nil {
			return Diagnosis{}, err
		}
	}
	if _, err := a.tool(ctx, run.ID, "read_evidence", map[string]string{"source": "all"}, func() (any, error) { return incident.Evidence, nil }); err != nil {
		return Diagnosis{}, err
	}
	if _, err := a.tool(ctx, run.ID, "search_runbooks", map[string]string{"query": incident.Type}, func() (any, error) { return SearchRunbooks(incident.Type, incident.Type), nil }); err != nil {
		return Diagnosis{}, err
	}
	d := RuleDiagnosis(incident)
	if a.model != nil {
		d.Limitations += " 模型失败或达到工具限制，已降级规则分析，本任务不再自动写入。"
	}
	_, err := a.tool(ctx, run.ID, "submit_diagnosis", map[string]string{"engine": "rules"}, func() (any, error) { return d, nil })
	return d, err
}
