package core

import (
	"errors"
	"slices"
)

// These names are also the wire protocol between platform and Connector.
func DiagnosticChecks(kind string) []string {
	switch kind {
	case "Service":
		return []string{"service_backends", "target_events"}
	case "Deployment":
		return []string{"rollout_status", "container_exits", "target_events"}
	default:
		return nil
	}
}

// Completed checks have target-bound evidence IDs assigned by the Connector.
func RemainingDiagnosticChecks(i Incident) []string {
	remaining := []string{}
	for _, check := range DiagnosticChecks(i.Target.Kind) {
		id := "check-" + check + "-" + i.Target.UID
		if !slices.ContainsFunc(i.Evidence, func(e Evidence) bool { return e.ID == id }) {
			remaining = append(remaining, check)
		}
	}
	return remaining
}

func validateChecks(requested, available []string) error {
	if len(requested) == 0 {
		return errors.New("继续调查需要选择尚未执行的专项检查")
	}
	for n, check := range requested {
		if !slices.Contains(available, check) || slices.Contains(requested[:n], check) {
			return errors.New("模型请求了不适用或重复的诊断检查")
		}
	}
	return nil
}

func ValidateTriage(first Investigation, i Incident) error {
	if first.Assessment == nil {
		return errors.New("模型未提交初诊判断")
	}
	if err := ValidateDiagnosis(*first.Assessment, i.Evidence); err != nil {
		return err
	}
	switch first.Assessment.Disposition {
	case "consider_plan", "need_evidence":
		if len(first.Checks) != 0 {
			return errors.New("直接给出结论时不能同时请求补查")
		}
	case "inspect":
		if len(first.Checks) == 0 || len(first.Hypotheses) == 0 {
			return errors.New("补查需要候选原因和明确的检查项目")
		}
		return validateChecks(first.Checks, RemainingDiagnosticChecks(i))
	default:
		return errors.New("未识别的初诊决定")
	}
	return nil
}

func ValidateCheckDecision(d CheckDecision, i Incident, freshIDs []string) error {
	if err := ValidateDiagnosis(d.Diagnosis, i.Evidence); err != nil {
		return err
	}
	switch d.Disposition {
	case "inspect":
		if err := validateChecks(d.Checks, RemainingDiagnosticChecks(i)); err != nil {
			return err
		}
	case "consider_plan", "need_evidence":
		if len(d.Checks) != 0 {
			return errors.New("结束诊断时不能同时请求补查")
		}
	default:
		return errors.New("无法确定专项补查后的诊断结论")
	}
	if d.Disposition != "need_evidence" && !slices.ContainsFunc(d.EvidenceIDs, func(id string) bool { return slices.Contains(freshIDs, id) }) {
		return errors.New("补查后的判断未引用本轮新增证据，停止自动调查")
	}
	return nil
}

type CheckResult struct {
	Target   Target   `json:"target"`
	Evidence Evidence `json:"evidence"`
}
