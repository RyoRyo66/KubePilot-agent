package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"kubeagent/internal/core"
)

func (s *Service) followUp(ctx context.Context, id string, m ModelClient, i core.Incident, first core.Investigation) (core.Diagnosis, error) {
	var empty core.Diagnosis
	// Even if a model requests a write tool, only these checks may become jobs.
	if err := core.ValidateTriage(first, i); err != nil {
		return empty, err
	}
	checks := first.Checks
	// Every inspect decision must consume at least one unused check for this target.
	for len(checks) > 0 {
		freshIDs := make([]string, 0, len(checks))
		for _, check := range checks {
			if err := ctx.Err(); err != nil {
				return empty, err
			}
			e, err := s.collectDiagnosticCheck(ctx, id, check, i)
			if err != nil {
				return empty, err
			}
			i.Evidence = append(i.Evidence, e)
			freshIDs = append(freshIDs, e.ID)
			if err = s.caseUpdate(id, "diagnosing", "已收到专项证据", e.ID, func(c *Case) { c.Incident.Evidence = i.Evidence }); err != nil {
				return empty, err
			}
		}
		decision, err := m.AssessChecks(ctx, i, first, freshIDs)
		if err != nil {
			return empty, fmt.Errorf("模型综合诊断失败：%w", err)
		}
		if err = core.ValidateCheckDecision(decision, i, freshIDs); err != nil {
			return empty, err
		}
		if err = s.caseUpdate(id, "diagnosing", "补查判断已记录", decision.Conclusion, func(c *Case) {
			c.Investigation.FollowUps = append(c.Investigation.FollowUps, decision)
		}); err != nil {
			return empty, err
		}
		if decision.Disposition != "inspect" {
			return decision.Diagnosis, nil
		}
		first.FollowUps = append(first.FollowUps, decision)
		checks = decision.Checks
	}
	return empty, errors.New("没有可执行的专项检查")
}

func (s *Service) collectDiagnosticCheck(ctx context.Context, id, check string, i core.Incident) (core.Evidence, error) {
	var empty core.Evidence
	if err := s.caseUpdate(id, "diagnosing", "模型请求专项补查", check, nil); err != nil {
		return empty, err
	}
	r, err := s.call(ctx, check, &i, nil)
	if err != nil {
		return empty, fmt.Errorf("专项补查 %s 失败：%w", check, err)
	}
	if !r.OK || r.Check == nil || r.Check.Target != i.Target {
		return empty, errors.New("专项结果缺失或对象版本已变化，停止生成计划")
	}
	e := r.Check.Evidence
	if e.ID != "check-"+check+"-"+i.Target.UID || strings.TrimSpace(e.Detail) == "" || len(e.Detail) > 40000 || e.At.IsZero() {
		return empty, errors.New("专项证据缺少有效编号、时间或内容")
	}
	for _, existing := range i.Evidence {
		if existing.ID == e.ID {
			return empty, errors.New("专项证据编号重复")
		}
	}
	return e, nil
}
