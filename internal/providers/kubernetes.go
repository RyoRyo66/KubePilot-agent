package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"kubeagent/internal/core"
)

type KubeConfig struct{ Kubeconfig, Context, Namespace, BaselinePath, ProbeConfig string }
type probeSet struct {
	Readiness *corev1.Probe `json:"readiness"`
	Liveness  *corev1.Probe `json:"liveness"`
	Startup   *corev1.Probe `json:"startup"`
}
type Baseline struct {
	ID         string              `json:"id"`
	Target     core.Target         `json:"target"`
	CapturedAt time.Time           `json:"capturedAt"`
	Images     map[string]string   `json:"images,omitempty"`
	Probes     map[string]probeSet `json:"probes,omitempty"`
	Selector   map[string]string   `json:"selector,omitempty"`
}
type Kubernetes struct {
	Client         kubernetes.Interface
	Config         KubeConfig
	mu             sync.RWMutex
	baselines      map[string]Baseline
	probeURLs      map[string]string
	ProbeClient    *http.Client
	VerifyInterval time.Duration
	VerifyTimeout  time.Duration
}

func NewKubernetes(config KubeConfig) (*Kubernetes, error) {
	if config.Namespace == "" || config.Namespace == "kube-system" || config.Namespace == "default" {
		return nil, errors.New("请明确指定独立实验命名空间，不使用 default 或 kube-system")
	}
	if config.Context == "" {
		return nil, errors.New("真实模式必须明确配置 AGENT_KUBE_CONTEXT，不自动选用当前集群")
	}
	var rc *rest.Config
	var err error
	if config.Context == "in-cluster" {
		rc, err = rest.InClusterConfig()
	} else {
		if config.Kubeconfig == "" {
			return nil, errors.New("请配置 AGENT_KUBECONFIG")
		}
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: config.Kubeconfig}
		rc, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: config.Context}).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("集群配置无效: %w", err)
	}
	rc.Timeout = 15 * time.Second
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	k := &Kubernetes{Client: client, Config: config, baselines: map[string]Baseline{}, probeURLs: map[string]string{}, VerifyInterval: 2 * time.Second, VerifyTimeout: 30 * time.Second}
	if bytes, err := os.ReadFile(config.BaselinePath); err == nil {
		if err = json.Unmarshal(bytes, &k.baselines); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if config.ProbeConfig != "" {
		bytes, err := os.ReadFile(config.ProbeConfig)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(bytes, &k.probeURLs); err != nil {
			return nil, err
		}
	}
	for _, value := range k.probeURLs {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return nil, errors.New("业务探测地址必须为不含凭证的 http(s) URL")
		}
	}
	return k, nil
}

func target(kind, namespace, name, uid, version, container string) core.Target {
	return core.Target{Kind: kind, Namespace: namespace, Name: name, UID: uid, Version: version, Container: container}
}
func podReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

var secretPattern = regexp.MustCompile(`(?i)(authorization|password|passwd|token|api[_-]?key|secret)(\s*[:=]\s*|\s+)([^\s,;]+)`)
var authHeaderPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*)(?:basic|bearer)\s+[^\s,;]+`)

func redact(value string) string {
	return secretPattern.ReplaceAllString(authHeaderPattern.ReplaceAllString(value, "${1}[REDACTED]"), "$1$2[REDACTED]")
}

func (k *Kubernetes) Collect(ctx context.Context) (core.Snapshot, error) {
	ns := k.Config.Namespace
	s := core.Snapshot{At: time.Now().UTC(), Cluster: k.Config.Context, Nodes: -1, ReadyNodes: -1, Resources: []core.Resource{}, Findings: []core.Finding{}, Warnings: []string{}}
	pods, err := k.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return s, fmt.Errorf("Pod 采集失败: %w", err)
	}
	deployments, err := k.Client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return s, fmt.Errorf("Deployment 采集失败: %w", err)
	}
	replicasets, err := k.Client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return s, err
	}
	services, err := k.Client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return s, err
	}
	endpoints, err := k.Client.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return s, err
	}
	events, eventErr := k.Client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
	if eventErr != nil {
		s.Warnings = append(s.Warnings, "事件采集失败，当前诊断证据不完整。")
	}
	nodes, nodeErr := k.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if nodeErr != nil {
		s.Warnings = append(s.Warnings, "无法读取集群节点（可能未授予集群级只读权限）；节点数量显示为空。")
	} else {
		s.Nodes = len(nodes.Items)
		s.ReadyNodes = 0
		for _, n := range nodes.Items {
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
					s.ReadyNodes++
				}
			}
		}
	}
	depMap := map[string]appsv1.Deployment{}
	rsOwners := map[string]string{}
	for _, rs := range replicasets.Items {
		for _, o := range rs.OwnerReferences {
			if o.Kind == "Deployment" {
				rsOwners[rs.Name] = o.Name
			}
		}
	}
	for _, d := range deployments.Items {
		depMap[d.Name] = d
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		status := "healthy"
		if d.Status.ReadyReplicas != desired || d.Status.ObservedGeneration < d.Generation {
			status = "warning"
		}
		s.Resources = append(s.Resources, core.Resource{Name: d.Name, Kind: "Deployment", Namespace: ns, Ready: d.Status.ReadyReplicas, Desired: desired, Status: status})
	}
	s.Pods = len(pods.Items)
	s.Services = len(services.Items)
	seen := map[string]bool{}
	for _, p := range pods.Items {
		if p.DeletionTimestamp != nil {
			continue
		}
		if podReady(p) {
			s.ReadyPods++
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		t := target("Pod", ns, p.Name, string(p.UID), p.ResourceVersion, "")
		for _, o := range p.OwnerReferences {
			if o.Kind == "ReplicaSet" {
				if d, ok := depMap[rsOwners[o.Name]]; ok {
					t = target("Deployment", ns, d.Name, string(d.UID), d.ResourceVersion, "")
				}
			}
		}
		fault := ""
		detail := ""
		evidence := []core.Evidence{}
		for _, c := range p.Status.ContainerStatuses {
			if c.Ready {
				continue
			}
			if c.State.Waiting != nil {
				reason := c.State.Waiting.Reason
				if reason == "ImagePullBackOff" || reason == "ErrImagePull" {
					fault = "ImagePullBackOff"
				} else if reason == "CrashLoopBackOff" {
					fault = "CrashLoopBackOff"
				}
				detail = reason + ": " + c.State.Waiting.Message
			}
			if c.LastTerminationState.Terminated != nil && c.LastTerminationState.Terminated.Reason == "OOMKilled" {
				fault = "OOMKilled"
				detail = fmt.Sprintf("Last termination: OOMKilled, exitCode=%d", c.LastTerminationState.Terminated.ExitCode)
			}
			if fault != "" {
				t.Container = c.Name
				break
			}
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == "Unschedulable" {
				fault = "Pending"
				detail = c.Message
			}
		}
		if eventErr == nil {
			for _, event := range events.Items {
				if event.InvolvedObject.UID != p.UID {
					continue
				}
				if event.Reason == "Unhealthy" && fault != "ImagePullBackOff" && fault != "Pending" && fault != "OOMKilled" {
					// Only recent events for the currently unready Pod are considered.
					at := event.LastTimestamp.Time
					if at.IsZero() {
						at = event.EventTime.Time
					}
					if at.IsZero() {
						at = event.CreationTimestamp.Time
					}
					if time.Since(at) < 10*time.Minute {
						fault = "ProbeFailure"
						t.Container = ""
						field := event.InvolvedObject.FieldPath
						if strings.HasPrefix(field, "spec.containers{") {
							candidate := strings.TrimSuffix(strings.TrimPrefix(field, "spec.containers{"), "}")
							for _, container := range p.Spec.Containers {
								if container.Name == candidate {
									t.Container = candidate
								}
							}
						}
						if t.Container == "" && len(p.Spec.Containers) == 1 {
							t.Container = p.Spec.Containers[0].Name
						}
					}
				}
				if len(evidence) < 8 && event.Type == "Warning" {
					evidence = append(evidence, core.Evidence{ID: "event-" + string(event.UID), Source: "Kubernetes Event", Summary: event.Reason, Detail: redact(event.Message), At: s.At})
				}
			}
		}
		if fault == "" {
			continue
		}
		key := ns + "/" + t.Kind + "/" + t.Name + "/" + t.Container + "/" + fault
		if seen[key] {
			continue
		}
		seen[key] = true
		evidence = append(evidence, core.Evidence{ID: "status-" + string(p.UID), Source: "Pod Status", Summary: "当前对象状态", Detail: redact(fmt.Sprintf("Pod=%s; Phase=%s; %s", p.Name, p.Status.Phase, detail)), At: s.At})
		if t.Container != "" && fault != "ImagePullBackOff" {
			tail, limit := int64(40), int64(4096)
			data, logErr := k.Client.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{Container: t.Container, Previous: fault == "CrashLoopBackOff" || fault == "OOMKilled", TailLines: &tail, LimitBytes: &limit}).DoRaw(ctx)
			if logErr == nil {
				evidence = append(evidence, core.Evidence{ID: "logs-" + string(p.UID), Source: "Container Log", Summary: "最近容器日志（已过滤常见凭证字段）", Detail: redact(string(data)), At: s.At})
			}
		}
		title := fault
		for _, book := range core.Runbooks {
			if book.Type == fault {
				title = book.Title
			}
		}
		severity := "warning"
		if fault == "ImagePullBackOff" || fault == "ProbeFailure" || fault == "CrashLoopBackOff" {
			severity = "critical"
		}
		s.Findings = append(s.Findings, core.Finding{Key: key, Type: fault, Title: title, Severity: severity, Target: t, Evidence: evidence})
	}
	// A failed rollout can have no Pod at all, e.g. ReplicaSet creation rejected by quota.
	withFinding := map[string]bool{}
	for _, f := range s.Findings {
		if f.Target.Kind == "Deployment" {
			withFinding[f.Target.UID] = true
		}
	}
	for _, d := range deployments.Items {
		if d.UID == "" || withFinding[string(d.UID)] || d.DeletionTimestamp != nil || d.Spec.Paused || d.Status.ObservedGeneration < d.Generation {
			continue
		}
		for _, c := range d.Status.Conditions {
			failed := c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue || c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse && c.Reason == "ProgressDeadlineExceeded"
			if !failed {
				continue
			}
			t := target("Deployment", ns, d.Name, string(d.UID), d.ResourceVersion, "")
			e := core.Evidence{ID: "rollout-" + t.UID, Source: "Deployment Condition", Summary: c.Reason, Detail: redact(fmt.Sprintf("%s=%s: %s", c.Type, c.Status, c.Message)), At: s.At}
			s.Findings = append(s.Findings, core.Finding{Key: ns + "/Deployment/" + d.Name + "/RolloutFailure", Type: "RolloutFailure", Title: "Deployment 发布失败", Severity: "warning", Target: t, Evidence: []core.Evidence{e}})
			break
		}
	}
	for _, svc := range services.Items {
		if len(svc.Spec.Selector) == 0 || svc.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}
		count := 0
		for _, slice := range endpoints.Items {
			if slice.Labels["kubernetes.io/service-name"] == svc.Name {
				for _, ep := range slice.Endpoints {
					if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
						count++
					}
				}
			}
		}
		if count > 0 || time.Since(svc.CreationTimestamp.Time) < 30*time.Second {
			continue
		}
		selector, _ := json.Marshal(svc.Spec.Selector)
		t := target("Service", ns, svc.Name, string(svc.UID), svc.ResourceVersion, "")
		evidence := []core.Evidence{{ID: "service-" + string(svc.UID), Source: "Service / EndpointSlice", Summary: "未发现就绪端点", Detail: "Selector=" + string(selector) + "；就绪端点数为 0。需区分选择器错误与后端未就绪。", At: s.At}}
		if b, err := k.baseline(t); err == nil && len(b.Selector) > 0 {
			matched, ready := 0, 0
			selector := labels.SelectorFromSet(b.Selector)
			for _, pod := range pods.Items {
				if !selector.Matches(labels.Set(pod.Labels)) || pod.DeletionTimestamp != nil {
					continue
				}
				matched++
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue && pod.Status.Phase == corev1.PodRunning {
						ready++
						break
					}
				}
			}
			evidence = append(evidence, core.Evidence{ID: "baseline-" + string(svc.UID), Source: "Verified baseline / live Pod status", Summary: "同一 Service 的健康基线与当前后端就绪状态", Detail: fmt.Sprintf("BaselineID=%s; CapturedAt=%s; UID=%s; BaselineSelector=%s; 当前匹配基线选择器的 Pod=%d; 其中就绪 Pod=%d。", b.ID, b.CapturedAt.Format(time.RFC3339), b.Target.UID, compact(b.Selector), matched, ready), At: s.At})
		}
		s.Findings = append(s.Findings, core.Finding{Key: ns + "/Service/" + svc.Name + "/ServiceSelector", Type: "ServiceSelector", Title: "服务无就绪端点", Severity: "critical", Target: t, Evidence: evidence})
	}
	return s, nil
}

func (k *Kubernetes) businessProbe(ctx context.Context, kind, name string) error {
	u := k.probeURLs[kind+"/"+name]
	if u == "" {
		return errors.New("未配置该资源的业务探测 URL，不能确认健康基线或业务恢复")
	}
	client := k.ProbeClient
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("业务探测请求失败")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("业务探测返回 HTTP %d", res.StatusCode)
	}
	return nil
}
func deploymentReady(d *appsv1.Deployment) bool {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	return desired > 0 && d.DeletionTimestamp == nil && !d.Spec.Paused && d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == desired && d.Status.ReadyReplicas == desired && d.Status.AvailableReplicas == desired
}
func (k *Kubernetes) serviceReady(ctx context.Context, name string) (bool, error) {
	slices, err := k.Client.DiscoveryV1().EndpointSlices(k.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + name})
	if err != nil {
		return false, err
	}
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
				return true, nil
			}
		}
	}
	return false, nil
}

func (k *Kubernetes) CaptureBaseline(ctx context.Context, kind, name string) error {
	if name == "" {
		return errors.New("资源名称不能为空")
	}
	b := Baseline{CapturedAt: time.Now().UTC(), Images: map[string]string{}, Probes: map[string]probeSet{}}
	switch kind {
	case "Deployment":
		d, err := k.Client.AppsV1().Deployments(k.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !deploymentReady(d) {
			return errors.New("工作负载未就绪，不记录健康基线")
		}
		b.Target = target(kind, k.Config.Namespace, name, string(d.UID), d.ResourceVersion, "")
		selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
		if err != nil {
			return err
		}
		pods, err := k.Client.CoreV1().Pods(k.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return err
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			b.Images[c.Name] = c.Image
			for _, p := range pods.Items {
				if !podReady(p) || p.DeletionTimestamp != nil {
					continue
				}
				matches := false
				for _, pc := range p.Spec.Containers {
					if pc.Name == c.Name && pc.Image == c.Image {
						matches = true
					}
				}
				if !matches {
					continue
				}
				for _, cs := range p.Status.ContainerStatuses {
					image := strings.TrimPrefix(cs.ImageID, "docker-pullable://")
					if cs.Name == c.Name && strings.Contains(image, "@sha256:") {
						b.Images[c.Name] = image
					}
				}
			}
			b.Probes[c.Name] = probeSet{Readiness: c.ReadinessProbe.DeepCopy(), Liveness: c.LivenessProbe.DeepCopy(), Startup: c.StartupProbe.DeepCopy()}
		}
		if err := k.businessProbe(ctx, kind, name); err != nil {
			return err
		}
		current, err := k.Client.AppsV1().Deployments(k.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.ResourceVersion != b.Target.Version {
			return errors.New("记录基线期间对象发生变化，请重试")
		}
	case "Service":
		svc, err := k.Client.CoreV1().Services(k.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		ready, err := k.serviceReady(ctx, name)
		if err != nil {
			return err
		}
		if !ready || len(svc.Spec.Selector) == 0 {
			return errors.New("服务无就绪端点或没有选择器，不能记录基线")
		}
		b.Target = target(kind, k.Config.Namespace, name, string(svc.UID), svc.ResourceVersion, "")
		b.Selector = svc.Spec.Selector
		if err := k.businessProbe(ctx, kind, name); err != nil {
			return err
		}
		current, err := k.Client.CoreV1().Services(k.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.ResourceVersion != b.Target.Version {
			return errors.New("记录基线期间对象发生变化，请重试")
		}
	default:
		return errors.New("只支持 Deployment 和 Service 基线")
	}
	bytes, _ := json.Marshal(b)
	digest := sha256.Sum256(bytes)
	b.ID = hex.EncodeToString(digest[:])[:16]
	k.mu.Lock()
	defer k.mu.Unlock()
	next := map[string]Baseline{}
	for key, value := range k.baselines {
		next[key] = value
	}
	next[kind+"/"+name] = b
	bytes, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.Config.BaselinePath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(k.Config.BaselinePath), ".baseline-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(bytes); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), k.Config.BaselinePath); err != nil {
		return err
	}
	k.baselines = next
	return nil
}

func (k *Kubernetes) baseline(t core.Target) (Baseline, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	b, ok := k.baselines[t.Kind+"/"+t.Name]
	if !ok {
		return b, errors.New("缺少已验证的健康基线；请先在健康状态下记录基线")
	}
	if b.Target.UID != t.UID || b.Target.Namespace != t.Namespace {
		return b, errors.New("基线与当前对象身份不匹配")
	}
	return b, nil
}
func compact(value any) string { bytes, _ := json.Marshal(value); return string(bytes) }
func checkIdentity(t core.Target, meta metav1.Object) error {
	if t.UID != string(meta.GetUID()) || t.Version != meta.GetResourceVersion() {
		return errors.New("对象 UID 或 resourceVersion 已变化，请重新巡检诊断")
	}
	if meta.GetDeletionTimestamp() != nil {
		return errors.New("对象正在删除，不执行变更")
	}
	return nil
}
func (k *Kubernetes) Plan(ctx context.Context, i core.Incident) (*core.Plan, error) {
	if i.Target.Namespace != k.Config.Namespace {
		return nil, errors.New("目标命名空间不在允许范围")
	}
	action := ""
	for _, book := range core.Runbooks {
		if book.Type == i.Type {
			action = book.Action
		}
	}
	if action == "" {
		return nil, errors.New("该场景未开放自动修复，请根据证据人工处理")
	}
	b, err := k.baseline(i.Target)
	if err != nil {
		return nil, err
	}
	p := &core.Plan{Action: action, Summary: "恢复已验证健康基线中的限定字段", BaselineID: b.ID, Target: i.Target, Checks: []string{"命名空间及动作白名单", "健康基线与 UID 匹配", "resourceVersion 并发校验", "服务端 dry-run", "就绪状态与业务探测", "连续三次稳定验证"}}
	if action == "restore_selector" {
		if i.Target.Kind != "Service" {
			return nil, errors.New("动作与资源类型不匹配")
		}
		svc, err := k.Client.CoreV1().Services(k.Config.Namespace).Get(ctx, i.Target.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if err = checkIdentity(i.Target, svc); err != nil {
			return nil, err
		}
		if len(b.Selector) == 0 || reflect.DeepEqual(svc.Spec.Selector, b.Selector) {
			return nil, errors.New("选择器与基线一致或基线无效，应继续检查后端就绪状态")
		}
		p.Before = compact(svc.Spec.Selector)
		p.After = compact(b.Selector)
	} else {
		if i.Target.Kind != "Deployment" {
			return nil, errors.New("仅支持已定位到 Deployment 的受控恢复")
		}
		d, err := k.Client.AppsV1().Deployments(k.Config.Namespace).Get(ctx, i.Target.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if err = checkIdentity(i.Target, d); err != nil {
			return nil, err
		}
		found := false
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name != i.Target.Container {
				continue
			}
			found = true
			if action == "restore_image" {
				image := b.Images[c.Name]
				if !strings.Contains(image, "@sha256:") {
					return nil, errors.New("基线镜像没有固定 digest，不能自动恢复可变标签")
				}
				if image == c.Image {
					return nil, errors.New("镜像与健康基线一致，应检查仓库权限或网络")
				}
				p.Before = c.Image
				p.After = image
			} else {
				old := probeSet{Readiness: c.ReadinessProbe, Liveness: c.LivenessProbe, Startup: c.StartupProbe}
				saved, ok := b.Probes[c.Name]
				if !ok || (saved.Readiness == nil && saved.Liveness == nil && saved.Startup == nil) || reflect.DeepEqual(old, saved) {
					return nil, errors.New("未发现可恢复的探针配置差异，应检查应用健康状态")
				}
				if (old.Readiness != nil && saved.Readiness == nil) || (old.Liveness != nil && saved.Liveness == nil) || (old.Startup != nil && saved.Startup == nil) {
					return nil, errors.New("拒绝通过移除探针修复故障")
				}
				p.Before = compact(old)
				p.After = compact(saved)
			}
		}
		if !found {
			return nil, errors.New("目标容器已不存在")
		}
	}
	return p, nil
}
func (k *Kubernetes) Execute(ctx context.Context, p core.Plan) (string, error) {
	fault := map[string]string{"restore_image": "ImagePullBackOff", "restore_selector": "ServiceSelector", "restore_probe": "ProbeFailure"}[p.Action]
	if fault == "" {
		return "", errors.New("动作不在白名单中")
	}
	fresh, err := k.Plan(ctx, core.Incident{Type: fault, Target: p.Target})
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(fresh, &p) {
		return "", errors.New("基线或处置参数已变化")
	}
	b, err := k.baseline(p.Target)
	if err != nil {
		return "", err
	}
	if p.Action == "restore_selector" {
		svc, err := k.Client.CoreV1().Services(k.Config.Namespace).Get(ctx, p.Target.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		if err = checkIdentity(p.Target, svc); err != nil {
			return "", err
		}
		svc.Spec.Selector = b.Selector
		if _, err = k.Client.CoreV1().Services(k.Config.Namespace).Update(ctx, svc, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
			return "", fmt.Errorf("服务端预检失败: %w", err)
		}
		if _, err = k.Client.CoreV1().Services(k.Config.Namespace).Update(ctx, svc, metav1.UpdateOptions{}); err != nil {
			return "", err
		}
	} else {
		d, err := k.Client.AppsV1().Deployments(k.Config.Namespace).Get(ctx, p.Target.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		if err = checkIdentity(p.Target, d); err != nil {
			return "", err
		}
		for j := range d.Spec.Template.Spec.Containers {
			c := &d.Spec.Template.Spec.Containers[j]
			if c.Name != p.Target.Container {
				continue
			}
			if p.Action == "restore_image" {
				c.Image = b.Images[c.Name]
			} else {
				probes := b.Probes[c.Name]
				c.ReadinessProbe = probes.Readiness.DeepCopy()
				c.LivenessProbe = probes.Liveness.DeepCopy()
				c.StartupProbe = probes.Startup.DeepCopy()
			}
		}
		if _, err = k.Client.AppsV1().Deployments(k.Config.Namespace).Update(ctx, d, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
			return "", fmt.Errorf("服务端预检失败: %w", err)
		}
		if _, err = k.Client.AppsV1().Deployments(k.Config.Namespace).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
			return "", err
		}
	}
	return "服务端预检通过，配置更新请求已提交。仍需等待工作负载就绪与业务探测通过。", nil
}
func (k *Kubernetes) Verify(ctx context.Context, i core.Incident) (bool, string, error) {
	if i.Target.Namespace != k.Config.Namespace {
		return false, "", errors.New("目标命名空间不在允许范围")
	}
	interval, timeout := k.VerifyInterval, k.VerifyTimeout
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	streak := 0
	last := "工作负载或业务探测尚未稳定"
	for {
		ready := false
		if i.Target.Kind == "Deployment" {
			d, err := k.Client.AppsV1().Deployments(k.Config.Namespace).Get(ctx, i.Target.Name, metav1.GetOptions{})
			if err != nil {
				return false, "", err
			}
			if string(d.UID) != i.Target.UID {
				return false, "对象已被替换，不能将新对象视为原事件恢复", nil
			}
			ready = deploymentReady(d)
		} else if i.Target.Kind == "Service" {
			svc, err := k.Client.CoreV1().Services(k.Config.Namespace).Get(ctx, i.Target.Name, metav1.GetOptions{})
			if err != nil {
				return false, "", err
			}
			if string(svc.UID) != i.Target.UID {
				return false, "服务对象已被替换", nil
			}
			ready, err = k.serviceReady(ctx, i.Target.Name)
			if err != nil {
				return false, "", err
			}
		} else {
			return false, "暂不支持该资源类型的自动业务恢复确认", nil
		}
		if ready {
			if err := k.businessProbe(ctx, i.Target.Kind, i.Target.Name); err != nil {
				ready = false
				last = err.Error()
			}
		}
		if ready {
			streak++
		} else {
			streak = 0
		}
		if streak >= 3 {
			return true, "连续三次检查通过：对象身份未变、当前版本就绪、业务 HTTP 探测成功。", nil
		}
		select {
		case <-ctx.Done():
			return false, last + "；观察窗口已结束，转人工核验。", nil
		case <-ticker.C:
		}
	}
}
