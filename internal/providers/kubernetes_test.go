package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktest "k8s.io/client-go/testing"
	"kubeagent/internal/core"
)

func kubeFixture() (*Kubernetes, *fake.Clientset, core.Incident) {
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "lab", UID: types.UID("uid-1"), ResourceVersion: "5", Generation: 3}, Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example/app:broken"}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 3, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}}
	c := fake.NewSimpleClientset(d)
	t := target("Deployment", "lab", "app", "uid-1", "5", "app")
	k := &Kubernetes{Client: c, Config: KubeConfig{Namespace: "lab"}, baselines: map[string]Baseline{"Deployment/app": {ID: "baseline-1", Target: t, Images: map[string]string{"app": "example/app@sha256:" + strings.Repeat("a", 64)}, Probes: map[string]probeSet{}}}, probeURLs: map[string]string{}, VerifyInterval: time.Millisecond, VerifyTimeout: 10 * time.Millisecond}
	return k, c, core.Incident{Type: "ImagePullBackOff", Target: t}
}

func TestServiceEvidenceUsesLiveBackendReadinessAndMatchingBaselineIdentity(t *testing.T) {
	k, client, _ := kubeFixture()
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "lab", UID: "svc-id", ResourceVersion: "1", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "wrong"}}}
	if _, err := client.CoreV1().Services("lab").Create(context.Background(), svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app-pod", Namespace: "lab", UID: "pod-id", Labels: map[string]string{"app": "app"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if _, err := client.CoreV1().Pods("lab").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	k.baselines["Service/svc"] = Baseline{ID: "trusted-baseline", Target: target("Service", "lab", "svc", "svc-id", "0", ""), CapturedAt: time.Now().Add(-time.Minute), Selector: map[string]string{"app": "app"}}
	for _, scenario := range []string{"ready", "unready", "replaced"} {
		if scenario == "unready" {
			pod.Status.Conditions[0].Status = corev1.ConditionFalse
			if _, err := client.CoreV1().Pods("lab").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if scenario == "replaced" {
			b := k.baselines["Service/svc"]
			b.Target.UID = "old-identity"
			k.baselines["Service/svc"] = b
		}
		snapshot, err := k.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var finding *core.Finding
		for i := range snapshot.Findings {
			if snapshot.Findings[i].Target.Name == "svc" {
				finding = &snapshot.Findings[i]
				break
			}
		}
		if finding == nil {
			t.Fatal("missing service finding")
		}
		if scenario == "replaced" {
			if len(finding.Evidence) != 1 {
				t.Fatal("stale baseline presented as trusted")
			}
			continue
		}
		want := "就绪 Pod=1"
		if scenario == "unready" {
			want = "就绪 Pod=0"
		}
		if len(finding.Evidence) != 2 || !strings.Contains(finding.Evidence[1].Detail, want) || !strings.Contains(finding.Evidence[1].Detail, "trusted-baseline") {
			t.Fatalf("incorrect live baseline evidence: %+v", finding.Evidence)
		}
	}
}
func TestKubernetesValidatesIdentityAndFixedDigest(t *testing.T) {
	for _, scenario := range []string{"wrong-uid", "stale-version", "wrong-namespace", "mutable-image", "missing-baseline"} {
		t.Run(scenario, func(t *testing.T) {
			k, c, i := kubeFixture()
			switch scenario {
			case "wrong-uid":
				i.Target.UID = "other"
			case "stale-version":
				i.Target.Version = "4"
			case "wrong-namespace":
				i.Target.Namespace = "prod"
			case "mutable-image":
				b := k.baselines["Deployment/app"]
				b.Images["app"] = "example/app:latest"
				k.baselines["Deployment/app"] = b
			case "missing-baseline":
				k.baselines = map[string]Baseline{}
			}
			if _, err := k.Plan(context.Background(), i); err == nil {
				t.Fatal("invalid plan accepted")
			}
			for _, a := range c.Actions() {
				if a.GetVerb() == "update" {
					t.Fatal("planning mutated cluster")
				}
			}
		})
	}
}
func TestDryRunFailurePreventsRealUpdate(t *testing.T) {
	k, c, i := kubeFixture()
	updates := 0
	c.PrependReactor("update", "deployments", func(action ktest.Action) (bool, runtime.Object, error) {
		updates++
		if len(action.(interface{ GetUpdateOptions() metav1.UpdateOptions }).GetUpdateOptions().DryRun) == 0 {
			t.Fatal("real write after failed dry run")
		}
		return true, nil, errors.New("admission denied")
	})
	plan, err := k.Plan(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = k.Execute(context.Background(), *plan); err == nil {
		t.Fatal("dry run rejection ignored")
	}
	if updates != 1 {
		t.Fatalf("updates=%d", updates)
	}
}
func TestKubernetesUpdatesOnlyAfterDryRun(t *testing.T) {
	k, c, i := kubeFixture()
	dryRuns, writes := 0, 0
	c.PrependReactor("update", "deployments", func(action ktest.Action) (bool, runtime.Object, error) {
		update := action.(ktest.UpdateAction)
		obj := update.GetObject().(*appsv1.Deployment)
		if obj.Spec.Template.Spec.Containers[0].Image != k.baselines["Deployment/app"].Images["app"] {
			t.Fatal("wrong image")
		}
		if len(action.(interface{ GetUpdateOptions() metav1.UpdateOptions }).GetUpdateOptions().DryRun) > 0 {
			dryRuns++
			return true, obj.DeepCopy(), nil
		}
		writes++
		return false, nil, nil
	})
	plan, err := k.Plan(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = k.Execute(context.Background(), *plan); err != nil {
		t.Fatal(err)
	}
	if dryRuns != 1 || writes != 1 {
		t.Fatalf("dryRuns=%d writes=%d", dryRuns, writes)
	}
}
func TestProbeRemovalIsRejected(t *testing.T) {
	k, c, i := kubeFixture()
	d, _ := c.AppsV1().Deployments("lab").Get(context.Background(), "app", metav1.GetOptions{})
	d.Spec.Template.Spec.Containers[0].LivenessProbe = &corev1.Probe{TimeoutSeconds: 1}
	d.Spec.Template.Spec.Containers[0].ReadinessProbe = &corev1.Probe{TimeoutSeconds: 1}
	_, _ = c.AppsV1().Deployments("lab").Update(context.Background(), d, metav1.UpdateOptions{})
	b := k.baselines["Deployment/app"]
	b.Probes["app"] = probeSet{Readiness: &corev1.Probe{TimeoutSeconds: 2}}
	k.baselines["Deployment/app"] = b
	i.Type = "ProbeFailure"
	if _, err := k.Plan(context.Background(), i); err == nil || !strings.Contains(err.Error(), "移除探针") {
		t.Fatalf("probe removal accepted: %v", err)
	}
}
func TestVerificationNeedsBusinessHealthAndIdentity(t *testing.T) {
	for _, scenario := range []string{"missing-probe", "failed-probe", "replaced-object", "healthy"} {
		t.Run(scenario, func(t *testing.T) {
			k, c, i := kubeFixture()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "failed-probe" {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(200)
				}
			}))
			defer server.Close()
			if scenario != "missing-probe" {
				k.probeURLs["Deployment/app"] = server.URL
			}
			if scenario == "replaced-object" {
				d, _ := c.AppsV1().Deployments("lab").Get(context.Background(), "app", metav1.GetOptions{})
				d.UID = "replacement"
				_, _ = c.AppsV1().Deployments("lab").Update(context.Background(), d, metav1.UpdateOptions{})
			}
			ok, _, err := k.Verify(context.Background(), i)
			if err != nil {
				t.Fatal(err)
			}
			if ok != (scenario == "healthy") {
				t.Fatalf("recovery incorrectly classified: %v", ok)
			}
		})
	}
}
func TestHealthyContainerHistoricalOOMIsNotCurrentFault(t *testing.T) {
	k, c, _ := kubeFixture()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app-pod", Namespace: "lab", UID: "pod-1"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "healthy", Ready: true, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}}, {Name: "other", Ready: false}}}}
	_, _ = c.CoreV1().Pods("lab").Create(context.Background(), p, metav1.CreateOptions{})
	snap, err := k.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range snap.Findings {
		if finding.Type == "OOMKilled" {
			t.Fatal("historical OOM treated as current failure")
		}
	}
}
func TestMissingEndpointsDoNotProveWrongSelector(t *testing.T) {
	k, c, _ := kubeFixture()
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "lab", UID: "svc-1", ResourceVersion: "1", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "app"}}}
	_, _ = c.CoreV1().Services("lab").Create(context.Background(), svc, metav1.CreateOptions{})
	target := target("Service", "lab", "svc", "svc-1", "1", "")
	k.baselines["Service/svc"] = Baseline{ID: "b", Target: target, Selector: map[string]string{"app": "app"}}
	if _, err := k.Plan(context.Background(), core.Incident{Type: "ServiceSelector", Target: target}); err == nil {
		t.Fatal("unchanged selector was modified")
	}
}
