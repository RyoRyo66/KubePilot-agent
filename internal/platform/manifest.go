package platform

import (
	"encoding/json"
	"errors"
	"sigs.k8s.io/yaml"
	"strconv"
	"strings"
)

func (s *Service) Manifest() (string, error) {
	c := s.current().Config
	if c.Generation == 0 {
		return "", errors.New("请先配置执行端授权")
	}
	if err := ValidateScope(c.Scope); err != nil {
		return "", err
	}
	ns := c.Scope.Namespace
	meta := func(name string) map[string]any { return map[string]any{"name": name, "namespace": ns} }
	cfg, _ := json.Marshal(ConnectorConfig{ServerURL: c.PublicURL, Token: c.ConnectorToken, AllowHTTP: c.AllowHTTP, Generation: c.Generation, Scope: c.Scope})
	rules := []any{
		map[string]any{"apiGroups": []string{""}, "resources": []string{"pods", "pods/log", "events", "services"}, "verbs": []string{"get", "list"}},
		map[string]any{"apiGroups": []string{"apps"}, "resources": []string{"deployments", "replicasets"}, "verbs": []string{"get", "list"}},
		map[string]any{"apiGroups": []string{"discovery.k8s.io"}, "resources": []string{"endpointslices"}, "verbs": []string{"get", "list"}},
	}
	for _, kind := range []string{"Deployment", "Service"} {
		names := []string{}
		for _, t := range c.Scope.Targets {
			if t.Kind == kind {
				names = append(names, t.Name)
			}
		}
		if len(names) > 0 {
			group, resource := "apps", "deployments"
			if kind == "Service" {
				group = ""
				resource = "services"
			}
			rules = append(rules, map[string]any{"apiGroups": []string{group}, "resources": []string{resource}, "resourceNames": names, "verbs": []string{"update"}})
		}
	}
	container := map[string]any{
		"name": "connector", "image": c.Scope.Image, "imagePullPolicy": "IfNotPresent",
		"args":            []string{"-config", "/etc/kubepilot/connector.json", "-data", "/data"},
		"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
		"resources":       map[string]any{"requests": map[string]string{"cpu": "50m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}},
		"volumeMounts":    []any{map[string]any{"name": "config", "mountPath": "/etc/kubepilot", "readOnly": true}, map[string]any{"name": "data", "mountPath": "/data"}},
	}
	pod := map[string]any{
		"serviceAccountName": "kubepilot-connector",
		"securityContext":    map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001},
		"containers":         []any{container},
		"volumes": []any{
			map[string]any{"name": "config", "secret": map[string]any{"secretName": "kubepilot-connector", "defaultMode": 288}},
			map[string]any{"name": "data", "persistentVolumeClaim": map[string]string{"claimName": "kubepilot-connector-data"}},
		},
	}
	template := map[string]any{
		"metadata": map[string]any{"labels": map[string]string{"app": "kubepilot-connector"}, "annotations": map[string]string{"kubepilot/config-generation": strconv.Itoa(c.Generation)}},
		"spec":     pod,
	}
	deployment := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("kubepilot-connector"),
		"spec": map[string]any{"replicas": 1, "strategy": map[string]string{"type": "Recreate"}, "selector": map[string]any{"matchLabels": map[string]string{"app": "kubepilot-connector"}}, "template": template},
	}
	docs := []any{
		map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta("kubepilot-connector")},
		map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": meta("kubepilot-connector"), "rules": rules},
		map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": meta("kubepilot-connector"), "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "kubepilot-connector", "namespace": ns}}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "kubepilot-connector"}},
		map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": meta("kubepilot-connector"), "type": "Opaque", "stringData": map[string]string{"connector.json": string(cfg)}},
		map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta("kubepilot-connector-data"), "spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": "1Gi"}}}},
		deployment,
	}
	var out strings.Builder
	out.WriteString("# 仅应用到已存在的授权命名空间。镜像需由平台管理员构建并发布。\n# 清单包含专用 Connector 令牌；不得提交到公开仓库。\n")
	for i, doc := range docs {
		b, err := json.Marshal(doc)
		if err != nil {
			return "", err
		}
		b, err = yaml.JSONToYAML(b)
		if err != nil {
			return "", err
		}
		if i > 0 {
			out.WriteString("---\n")
		}
		out.Write(b)
	}
	return out.String(), nil
}
