package providers

import (
	"context"
	"strings"
	"testing"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"kubeagent/internal/core"
)

func TestRolloutFailureInvestigatedWithoutPods(t *testing.T) {
	ctx := context.Background()
	k, client, incident := kubeFixture()
	ns := incident.Target.Namespace
	d, err := client.AppsV1().Deployments(ns).Get(ctx, "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Status.ReadyReplicas = 0
	d.Status.Conditions = []apps.DeploymentCondition{{Type: apps.DeploymentProgressing, Status: v1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}
	if _, err = client.AppsV1().Deployments(ns).UpdateStatus(ctx, d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	rs := &apps.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "new-revision", Namespace: ns, UID: "rs-owned", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(d, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})}}}
	if _, err = client.AppsV1().ReplicaSets(ns).Create(ctx, rs, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	own := v1.Event{ObjectMeta: metav1.ObjectMeta{Name: "quota", Namespace: ns}, InvolvedObject: v1.ObjectReference{Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID}, Reason: "FailedCreate", Type: "Warning", Message: "exceeded quota: memory; Authorization: Bearer hidden-value", LastTimestamp: metav1.Now()}
	other := own.DeepCopy()
	other.Name = "other"
	other.InvolvedObject.UID = "other-uid"
	other.Message = "unrelated workload details"
	for _, e := range []*v1.Event{&own, other} {
		if _, err = client.CoreV1().Events(ns).Create(ctx, e, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	client.ClearActions()
	snap, err := k.Collect(ctx)
	if err != nil || snap.Pods != 0 || len(snap.Findings) != 1 || snap.Findings[0].Type != "RolloutFailure" {
		t.Fatalf("missed failed rollout without pods: %v %+v", err, snap.Findings)
	}
	f := snap.Findings[0]
	if strings.Contains(f.Evidence[0].Detail, "quota") {
		t.Fatal("initial condition already contains the answer")
	}
	rollout, err := k.Inspect(ctx, f.Target, "rollout_status")
	if err != nil || !strings.Contains(rollout.Evidence.Detail, rs.Name) {
		t.Fatal("missing rollout revision", err)
	}
	events, err := k.Inspect(ctx, f.Target, "target_events")
	if err != nil || !strings.Contains(events.Evidence.Detail, "exceeded quota") || strings.Contains(events.Evidence.Detail, "hidden-value") || strings.Contains(events.Evidence.Detail, "unrelated") {
		t.Fatal("wrong scoped evidence", err)
	}
	if plan, e := k.Plan(ctx, core.Incident{Type: f.Type, Target: f.Target}); e == nil || plan != nil {
		t.Fatal("unsupported failure got an automatic plan")
	}
	bad := f.Target
	bad.UID = "old"
	if _, err = k.Inspect(ctx, bad, "rollout_status"); err == nil {
		t.Fatal("accepted obsolete UID")
	}
	if _, err = k.Inspect(ctx, f.Target, "execute"); err == nil {
		t.Fatal("accepted non-diagnostic action")
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "list" && a.GetVerb() != "get" {
			t.Fatalf("unexpected %s on %s", a.GetVerb(), a.GetResource().Resource)
		}
	}

	p := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: ns, UID: "pod-owned", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(rs, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"})}}, Status: v1.PodStatus{InitContainerStatuses: []v1.ContainerStatus{{Name: "init-config", RestartCount: 4, LastTerminationState: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 2, Reason: "Error", FinishedAt: metav1.NewTime(time.Now())}}}}}}
	if _, err = client.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	p = p.DeepCopy()
	p.Name = "outsider"
	p.UID = "unrelated"
	p.OwnerReferences = nil
	if _, err = client.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	exits, err := k.Inspect(ctx, f.Target, "container_exits")
	if err != nil || !strings.Contains(exits.Evidence.Detail, "init init-config") || !strings.Contains(exits.Evidence.Detail, "previousExit=2") || strings.Contains(exits.Evidence.Detail, "outsider") {
		t.Fatal("wrong container ownership or exit state", err)
	}
}
