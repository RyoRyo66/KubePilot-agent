package core

import "time"

type Runbook struct {
	Type       string   `json:"type"`
	Title      string   `json:"title"`
	Conclusion string   `json:"conclusion"`
	Checks     []string `json:"checks"`
	Action     string   `json:"action"`
	Source     string   `json:"source"`
}

var Runbooks = []Runbook{
	{"ImagePullBackOff", "镜像拉取失败", "容器镜像无法拉取。需结合镜像地址、事件及历史版本，区分标签错误、仓库权限和网络问题。", []string{"核对镜像名称、标签及拉取事件", "核对 imagePullSecrets 与仓库连通性，不读取或展示 Secret 值", "仅在存在可信健康基线时恢复镜像"}, "restore_image", "https://kubernetes.io/docs/concepts/containers/images/"},
	{"ServiceSelector", "服务端点异常", "Service 当前没有就绪端点。需要对比选择器、Pod 标签及就绪状态；没有端点不等于选择器一定错误。", []string{"对比 Service 选择器与就绪 Pod 标签", "检查 EndpointSlice 的 ready 条件", "检查业务探测，排除后端应用自身异常"}, "restore_selector", "https://kubernetes.io/docs/tasks/debug/debug-application/debug-service/"},
	{"ProbeFailure", "健康探针异常", "健康检查失败。需检查探针路径、端口、协议和应用实际健康状态，避免通过关闭探针掩盖故障。", []string{"核对 Unhealthy 事件与应用日志", "对比健康基线探针配置", "验证业务端点后持续观察"}, "restore_probe", "https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/"},
	{"CrashLoopBackOff", "容器反复退出", "容器反复退出是故障现象，需要前次容器日志、退出码与应用配置定位根因。此场景默认转人工处理。", []string{"查看前次容器日志和退出码", "核对配置引用及启动参数", "避免将反复重启视为根因修复"}, "", "https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/"},
	{"OOMKilled", "容器内存不足", "容器最近因 OOM 终止且仍存在异常。需区分资源限额过小、内存泄漏及节点压力，不能仅凭一次事件自动扩大限额。", []string{"核对终止原因、内存 limit 与使用趋势", "检查节点压力与同节点工作负载", "结合压测或应用分析确定处置方案"}, "", "https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/"},
	{"Pending", "工作负载调度受阻", "Pod 尚未完成调度。应根据 FailedScheduling 事件检查资源、污点、亲和性和存储条件。", []string{"核对调度事件及 PodScheduled 条件", "检查 requests、节点可用资源及污点", "检查 PVC 绑定与调度约束"}, "", "https://kubernetes.io/docs/tasks/debug/debug-application/debug-pods/"},
}

func RuleDiagnosis(incident Incident) Diagnosis {
	d := Diagnosis{Engine: "rules", At: time.Now().UTC(), EvidenceIDs: []string{}, Recommendations: []string{}, Limitations: "规则诊断不等同于确认根因；自动动作仍需可信基线与独立策略校验。"}
	for _, e := range incident.Evidence {
		d.EvidenceIDs = append(d.EvidenceIDs, e.ID)
	}
	for _, book := range Runbooks {
		if book.Type == incident.Type {
			d.Conclusion = book.Conclusion
			d.Recommendations = book.Checks
			return d
		}
	}
	d.Conclusion = "当前异常未匹配已知排障知识，保留证据并转人工处理。"
	return d
}
