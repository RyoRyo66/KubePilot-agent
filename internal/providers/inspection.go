package providers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"kubeagent/internal/core"
)

// Only pre-defined Kubernetes reads are accepted. Targets always come from the incident.
func (k *Kubernetes) Inspect(ctx context.Context, t core.Target, check string) (core.CheckResult, error) {
	var out core.CheckResult
	if t.Namespace != k.Config.Namespace || t.UID == "" || t.Version == "" || !slices.Contains(core.DiagnosticChecks(t.Kind), check) {
		return out, errors.New("补查对象或项目不在授权范围")
	}
	get := func() (metav1.Object, error) {
		if t.Kind == "Service" {
			return k.Client.CoreV1().Services(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		}
		return k.Client.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
	}
	obj, err := get()
	if err != nil {
		return out, err
	}
	if err = checkIdentity(t, obj); err != nil {
		return out, err
	}
	var details []string
	owners := map[types.UID]bool{obj.GetUID(): true}
	switch check {
	case "service_backends":
		details, err = k.serviceBackends(ctx, obj.(*v1.Service), t)
	case "container_exits", "rollout_status", "target_events":
		if d, ok := obj.(*appsv1.Deployment); ok {
			details, err = k.deploymentDetails(ctx, d, check, owners)
		}
		if err == nil && check == "target_events" {
			details, err = k.targetEvents(ctx, t.Namespace, owners)
		}
	}
	if err != nil {
		return out, err
	}
	// Do not combine evidence from an object that was replaced or changed during the reads.
	obj, err = get()
	if err != nil {
		return out, err
	}
	if err = checkIdentity(t, obj); err != nil {
		return out, err
	}
	return checkedObservation(t, check, details), nil
}

func (k *Kubernetes) deploymentDetails(ctx context.Context, d *appsv1.Deployment, check string, owners map[types.UID]bool) ([]string, error) {
	var details []string
	sets, err := k.Client.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	if check == "rollout_status" {
		details = append(details, fmt.Sprintf("generation=%d observed=%d replicas=%d updated=%d ready=%d available=%d unavailable=%d paused=%t", d.Generation, d.Status.ObservedGeneration, d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas, d.Status.UnavailableReplicas, d.Spec.Paused))
		for _, c := range d.Status.Conditions {
			details = append(details, fmt.Sprintf("%s=%s reason=%s", c.Type, c.Status, c.Reason))
		}
	}
	ownedSets := map[types.UID]bool{}
	for _, rs := range sets.Items {
		if rs.UID == "" || !metav1.IsControlledBy(&rs, d) {
			continue
		}
		ownedSets[rs.UID], owners[rs.UID] = true, true
		if check == "rollout_status" {
			details = append(details, fmt.Sprintf("ReplicaSet %s revision=%s replicas=%d ready=%d", rs.Name, rs.Annotations["deployment.kubernetes.io/revision"], rs.Status.Replicas, rs.Status.ReadyReplicas))
		}
	}
	if check == "rollout_status" {
		return details, nil
	}
	pods, e := k.Client.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{})
	if e != nil {
		return nil, e
	}
	for _, p := range pods.Items {
		owner := metav1.GetControllerOf(&p)
		if owner == nil || owner.Kind != "ReplicaSet" || !ownedSets[owner.UID] || p.UID == "" {
			continue
		}
		owners[p.UID] = true
		if check == "container_exits" {
			details = append(details, containerStates(p.Name, "init", p.Status.InitContainerStatuses)...)
			details = append(details, containerStates(p.Name, "container", p.Status.ContainerStatuses)...)
		}
	}
	return details, nil
}

func containerStates(podName, group string, statuses []v1.ContainerStatus) []string {
	var details []string
	for _, c := range statuses {
		line := fmt.Sprintf("Pod %s / %s %s ready=%t restarts=%d", podName, group, c.Name, c.Ready, c.RestartCount)
		if w := c.State.Waiting; w != nil {
			line += " waiting=" + w.Reason
		}
		if current := c.State.Terminated; current != nil {
			line += fmt.Sprintf(" currentExit=%d reason=%s", current.ExitCode, current.Reason)
		}
		if prev := c.LastTerminationState.Terminated; prev != nil {
			line += fmt.Sprintf(" previousExit=%d reason=%s finished=%s", prev.ExitCode, prev.Reason, prev.FinishedAt.UTC().Format(time.RFC3339))
		}
		details = append(details, line)
	}
	return details
}

func (k *Kubernetes) targetEvents(ctx context.Context, ns string, owners map[types.UID]bool) ([]string, error) {
	var details []string
	events, e := k.Client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		return nil, e
	}
	type entry struct {
		at     time.Time
		detail string
	}
	var recent []entry
	for _, event := range events.Items {
		if !owners[event.InvolvedObject.UID] {
			continue
		}
		at := event.LastTimestamp.Time
		if event.Series != nil {
			at = event.Series.LastObservedTime.Time
		}
		if at.IsZero() {
			at = event.EventTime.Time
		}
		if at.IsZero() {
			at = event.CreationTimestamp.Time
		}
		recent = append(recent, entry{at, fmt.Sprintf("%s %s/%s %s %s ×%d %s", at.UTC().Format(time.RFC3339), event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Type, event.Reason, event.Count, redact(event.Message))})
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].at.After(recent[j].at) })
	for _, item := range recent {
		details = append(details, item.detail)
	}
	return details, nil
}

func (k *Kubernetes) serviceBackends(ctx context.Context, svc *v1.Service, t core.Target) ([]string, error) {
	ns := svc.Namespace
	current := labels.SelectorFromSet(svc.Spec.Selector)
	pods, err := k.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	count := func(selector map[string]string) (int, int) {
		matched, ready := 0, 0
		filter := labels.SelectorFromSet(selector)
		for _, p := range pods.Items {
			if len(selector) == 0 || p.DeletionTimestamp != nil || !filter.Matches(labels.Set(p.Labels)) {
				continue
			}
			matched++
			if podReady(p) {
				ready++
			}
		}
		return matched, ready
	}
	matched, ready := count(svc.Spec.Selector)
	details := []string{fmt.Sprintf("selector=%s matchedPods=%d readyPods=%d", current, matched, ready)}
	if b, e := k.baseline(t); e == nil {
		m, r := count(b.Selector)
		details = append(details, fmt.Sprintf("baseline=%s selector=%s matchedPods=%d readyPods=%d", b.ID, labels.SelectorFromSet(b.Selector), m, r))
	}
	for _, p := range svc.Spec.Ports {
		details = append(details, fmt.Sprintf("Service port=%d/%s name=%s targetPort=%s", p.Port, p.Protocol, p.Name, p.TargetPort.String()))
	}
	list, err := k.Client.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"kubernetes.io/service-name": svc.Name}.String()})
	if err != nil {
		return nil, err
	}
	owned, readyEP, unready, unknown := 0, 0, 0, 0
	for _, slice := range list.Items {
		if !metav1.IsControlledBy(&slice, svc) {
			continue
		}
		owned++
		for _, ep := range slice.Endpoints {
			switch {
			case ep.Conditions.Ready == nil:
				unknown++
			case *ep.Conditions.Ready:
				readyEP++
			default:
				unready++
			}
		}
		for _, p := range slice.Ports {
			if p.Port != nil {
				details = append(details, fmt.Sprintf("EndpointSlice %s port=%d", slice.Name, *p.Port))
			}
		}
	}
	return append([]string{fmt.Sprintf("ownedSlices=%d readyEndpoints=%d unreadyEndpoints=%d unknownReadiness=%d", owned, readyEP, unready, unknown)}, details...), nil
}

func checkedObservation(t core.Target, check string, lines []string) core.CheckResult {
	count := len(lines)
	if count > 24 {
		lines = append(lines[:24], fmt.Sprintf("共 %d 项，仅展示前 24 项", count))
	}
	if count == 0 {
		lines = []string{"此次查询未返回匹配记录；无法据此确认根因。"}
	}
	for i, line := range lines {
		r := []rune(redact(line))
		if len(r) > 320 {
			line = string(r[:320]) + "…"
		} else {
			line = string(r)
		}
		lines[i] = line
	}
	return core.CheckResult{Target: t, Evidence: core.Evidence{ID: "check-" + check + "-" + t.UID, Source: "Connector / " + check, Summary: check, Detail: strings.Join(lines, "\n"), At: time.Now().UTC()}}
}
