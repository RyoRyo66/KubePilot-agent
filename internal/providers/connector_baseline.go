package providers

import (
	"context"
	"errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureBaseline never overwrites an existing baseline. A recreated object
// requires deliberate re-enrollment, not automatic relearning of its state.
func (k *Kubernetes) EnsureBaseline(ctx context.Context, kind, name string) error {
	k.mu.RLock()
	baseline, exists := k.baselines[kind+"/"+name]
	k.mu.RUnlock()
	if !exists {
		return k.CaptureBaseline(ctx, kind, name)
	}
	var uid string
	if kind == "Deployment" {
		d, e := k.Client.AppsV1().Deployments(k.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		uid = string(d.UID)
	} else if kind == "Service" {
		d, e := k.Client.CoreV1().Services(k.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		uid = string(d.UID)
	} else {
		return errors.New("不支持的基线对象")
	}
	if uid != baseline.Target.UID {
		return errors.New("资源已重建，旧健康基线无效；需人工核对后重新登记")
	}
	return nil
}
