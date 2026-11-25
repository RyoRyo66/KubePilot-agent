package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"kubeagent/internal/core"
)

type demoWorld struct {
	Faults  map[string]bool `json:"faults"`
	Version int             `json:"version"`
}

func (d *Demo) Inspect(ctx context.Context, t core.Target, check string) (core.CheckResult, error) {
	snapshot, err := d.Collect(ctx)
	if err != nil {
		return core.CheckResult{}, err
	}
	for _, f := range snapshot.Findings {
		if f.Target != t || !slices.Contains(core.DiagnosticChecks(t.Kind), check) {
			continue
		}
		var details []string
		switch check {
		case "service_backends":
			details = []string{"[模拟] 当前 selector app=payment-v2 匹配 0 个 Pod；基线 app=payment 匹配 2 个就绪 Pod；readyEndpoints=0。"}
		case "rollout_status":
			details = []string{"[模拟] 当前 Deployment 期望 2 个副本，就绪 0 个。"}
		case "container_exits":
			switch f.Type {
			case "OOMKilled":
				details = []string{"[模拟] previousExit=137 reason=OOMKilled"}
			case "CrashLoopBackOff":
				details = []string{"[模拟] previousExit=1 reason=Error"}
			}
		case "target_events":
			details = []string{"[模拟] 目标在本轮没有新增的 Kubernetes 事件。"}
		}
		return checkedObservation(t, check, details), nil
	}
	return core.CheckResult{}, errors.New("模拟对象或故障状态已变化，停止补查")
}

type Demo struct {
	mu        sync.Mutex
	world     demoWorld
	path      string
	namespace string
}

var demoNames = map[string]string{"ImagePullBackOff": "checkout-api", "ServiceSelector": "payment-service", "ProbeFailure": "inventory-api", "CrashLoopBackOff": "config-worker", "OOMKilled": "memory-worker", "Pending": "report-worker"}

func NewDemo(path, namespace string) (*Demo, error) {
	d := &Demo{path: path, namespace: namespace, world: demoWorld{Version: 1, Faults: map[string]bool{"ImagePullBackOff": true, "ProbeFailure": true, "Pending": true}}}
	b, err := os.ReadFile(path)
	if err == nil {
		d.world = demoWorld{}
		if err = json.Unmarshal(b, &d.world); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if d.world.Faults == nil {
		return nil, errors.New("无效的演示环境数据")
	}
	return d, nil
}
func (d *Demo) save(next demoWorld) error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(d.path), ".demo-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
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
	if err = os.Rename(f.Name(), d.path); err != nil {
		return err
	}
	d.world = next
	return nil
}
func (d *Demo) copyWorld() demoWorld {
	n := demoWorld{Version: d.world.Version, Faults: map[string]bool{}}
	for k, v := range d.world.Faults {
		n.Faults[k] = v
	}
	return n
}
func (d *Demo) Inject(fault string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := demoNames[fault]; !ok {
		return errors.New("不支持的故障类型")
	}
	if d.world.Faults[fault] {
		return errors.New("该故障已存在，请先处理或复位")
	}
	n := d.copyWorld()
	n.Faults[fault] = true
	n.Version++
	return d.save(n)
}
func (d *Demo) Reset() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.save(demoWorld{Version: d.world.Version + 1, Faults: map[string]bool{}})
}

func (d *Demo) Collect(ctx context.Context) (core.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return core.Snapshot{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().UTC()
	s := core.Snapshot{At: now, Cluster: "kind-graduation-demo", Nodes: 3, ReadyNodes: 3, Pods: 12, ReadyPods: 12, Services: 6, Resources: []core.Resource{}, Findings: []core.Finding{}, Warnings: []string{"当前为本地模拟环境，资源状态及事件均为演示数据。"}}
	for _, book := range core.Runbooks {
		fault, name := book.Type, demoNames[book.Type]
		ready := int32(2)
		status := "healthy"
		kind := "Deployment"
		if fault == "ServiceSelector" {
			kind = "Service"
		}
		if d.world.Faults[fault] {
			ready = 0
			status = "warning"
			if kind != "Service" {
				s.ReadyPods -= 2
			}
		}
		s.Resources = append(s.Resources, core.Resource{Name: name, Kind: kind, Namespace: d.namespace, Ready: ready, Desired: 2, Status: status})
		if !d.world.Faults[fault] {
			continue
		}
		target := core.Target{Kind: kind, Namespace: d.namespace, Name: name, UID: "demo-uid-" + name, Version: strconv.Itoa(d.world.Version), Container: "app"}
		detail := map[string]string{
			"ImagePullBackOff": "[模拟] Failed to pull image registry.demo/checkout:missing-tag: manifest unknown\n当前镜像：registry.demo/checkout:missing-tag\n健康基线：registry.demo/checkout@sha256:demo-verified-digest",
			"ServiceSelector":  "[模拟] Service selector: app=payment-v2\n健康 Pod labels: app=payment\nEndpointSlice ready endpoints: 0",
			"ProbeFailure":     "[模拟] Readiness probe failed: HTTP probe failed with statuscode: 404\n当前路径 /healthzz，基线路径 /healthz\n应用 /healthz 返回 200",
			"CrashLoopBackOff": "[模拟] Last termination: ExitCode=1\nPrevious log: required configuration APP_REGION is missing\n无法通过重启补齐未知配置，需人工确认有效值",
			"OOMKilled":        "[模拟] Last termination reason: OOMKilled\nMemory limit: 128Mi\n单次终止无法区分内存泄漏和容量不足",
			"Pending":          "[模拟] FailedScheduling: 0/3 nodes are available: 3 Insufficient cpu\nPod requests.cpu=8，当前实验节点不满足请求",
		}[fault]
		severity := "warning"
		if fault == "ImagePullBackOff" || fault == "ProbeFailure" || fault == "ServiceSelector" {
			severity = "critical"
		}
		s.Findings = append(s.Findings, core.Finding{Key: d.namespace + "/" + name + "/" + fault, Type: fault, Title: book.Title, Severity: severity, Target: target, Evidence: []core.Evidence{
			{ID: "ev-" + name + "-event", Source: "Kubernetes Event · 模拟", Summary: book.Title, Detail: detail, At: now},
			{ID: "ev-" + name + "-status", Source: "Resource Status · 模拟", Summary: "对象状态与基线对比", Detail: fmt.Sprintf("%s/%s，期望副本 2，就绪状态异常；版本 %s。", kind, name, target.Version), At: now},
		}})
	}
	return s, nil
}
func (d *Demo) Plan(ctx context.Context, i core.Incident) (*core.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.world.Faults[i.Type] {
		return nil, errors.New("当前异常已不存在，请验证恢复")
	}
	if i.Target.Version != strconv.Itoa(d.world.Version) {
		return nil, errors.New("演示对象版本已变化，请先重新巡检")
	}
	action := ""
	for _, b := range core.Runbooks {
		if b.Type == i.Type {
			action = b.Action
		}
	}
	if action == "" {
		return nil, errors.New("此场景没有足够的自动修复依据，需人工处置")
	}
	before := map[string]string{"restore_image": "image: registry.demo/checkout:missing-tag", "restore_selector": "selector: { app: payment-v2 }", "restore_probe": "readinessProbe.httpGet.path: /healthzz"}[action]
	after := map[string]string{"restore_image": "image: registry.demo/checkout@sha256:demo-verified-digest", "restore_selector": "selector: { app: payment }", "restore_probe": "readinessProbe.httpGet.path: /healthz"}[action]
	return &core.Plan{Action: action, Summary: "恢复已验证的演示基线，仅修改目标配置字段", Before: before, After: after, BaselineID: "demo-baseline-" + i.Target.Name, Target: i.Target, Checks: []string{"限定命名空间", "健康基线存在", "UID 与 resourceVersion 一致", "动作策略已启用", "恢复后进行模拟业务验证"}}, nil
}
func (d *Demo) Execute(ctx context.Context, p core.Plan) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	fault := map[string]string{"restore_image": "ImagePullBackOff", "restore_selector": "ServiceSelector", "restore_probe": "ProbeFailure"}[p.Action]
	if fault == "" || p.Target.Namespace != d.namespace || p.Target.Name != demoNames[fault] || p.Target.UID != "demo-uid-"+demoNames[fault] || p.Target.Version != strconv.Itoa(d.world.Version) {
		return "", errors.New("动作参数、对象身份或版本校验失败")
	}
	if !d.world.Faults[fault] {
		return "", errors.New("异常已不存在，不执行重复变更")
	}
	n := d.copyWorld()
	delete(n.Faults, fault)
	n.Version++
	if err := d.save(n); err != nil {
		return "", err
	}
	return "演示配置变更已提交；这不代表真实 Kubernetes 集群发生了修改。", nil
}
func (d *Demo) Verify(ctx context.Context, i core.Incident) (bool, string, error) {
	for pass := 0; pass < 2; pass++ {
		d.mu.Lock()
		active := d.world.Faults[i.Type]
		d.mu.Unlock()
		if active {
			return false, "模拟异常仍存在，尚未满足恢复条件。", nil
		}
		if pass == 0 {
			select {
			case <-ctx.Done():
				return false, "", ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return true, "演示验证：连续两次检查通过，模拟对象就绪及业务探测正常。真实恢复能力仍需在 kind 中验证。", nil
}
