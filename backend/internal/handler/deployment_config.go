package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func (h *KubernetesHandler) configurationClient() (kubernetes.Interface, error) {
	if h.configClient != nil {
		return h.configClient, nil
	}
	return k8s.GetClient()
}

func managedMicroservice(name string) (bool, error) {
	app, err := repository.NewApplicationRepo().FindByName(name)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return app.AppType == "java", nil
}

func deploymentConfigView(dep *appsv1.Deployment, managed bool) gin.H {
	return gin.H{"deployment": dep, "expectedStrategy": k8s.ExpectedDeploymentStrategy(dep.Name, managed), "waitSeconds": int64(k8s.DeploymentWaitTimeout(dep) / time.Second)}
}

func (h *KubernetesHandler) DeploymentConfig(c *gin.Context) {
	ns, ok := h.getNS(c)
	if !ok {
		return
	}
	cs, err := h.configurationClient()
	if err != nil {
		response.Fail(c, 12320, err.Error())
		return
	}
	dep, err := cs.AppsV1().Deployments(ns).Get(c.Request.Context(), c.Param("name"), metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12320, err.Error())
		return
	}
	managed, err := managedMicroservice(dep.Name)
	if err != nil {
		response.Fail(c, 12320, err.Error())
		return
	}
	response.OK(c, deploymentConfigView(dep, managed))
}

func (h *KubernetesHandler) PreviewDeploymentConfig(c *gin.Context) { h.editDeploymentConfig(c, true) }
func (h *KubernetesHandler) UpdateDeploymentConfig(c *gin.Context)  { h.editDeploymentConfig(c, false) }

func (h *KubernetesHandler) editDeploymentConfig(c *gin.Context, preview bool) {
	ns, ok := h.getNS(c)
	if !ok {
		return
	}
	var req k8s.DeploymentConfigEdit
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		response.Fail(c, 12321, "配置参数无效："+err.Error())
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		response.Fail(c, 12321, "请求只能包含一个配置对象")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if !preview {
		lockCtx, done := context.WithTimeout(ctx, 2*time.Second)
		unlock, err := k8s.LockDeployment(lockCtx, ns, c.Param("name"))
		done()
		if err != nil {
			response.Fail(c, 12321, "该 Deployment 正在执行任务，请稍后重试")
			return
		}
		defer unlock()
	}
	cs, err := h.configurationClient()
	if err != nil {
		response.Fail(c, 12321, err.Error())
		return
	}
	dep, err := cs.AppsV1().Deployments(ns).Get(ctx, c.Param("name"), metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12321, err.Error())
		return
	}
	managed, err := managedMicroservice(dep.Name)
	if err != nil {
		response.Fail(c, 12321, err.Error())
		return
	}
	next, diffs, err := k8s.PrepareDeploymentConfig(dep, req, managed)
	if err != nil {
		response.Fail(c, 12321, err.Error())
		return
	}
	if len(diffs) == 0 {
		response.OKWithMessage(c, "配置没有变化", gin.H{"deployment": dep, "changes": diffs, "rolloutRequired": false, "replicasChanged": false, "waitSeconds": int64(k8s.DeploymentWaitTimeout(dep) / time.Second)})
		return
	}
	validated, err := cs.AppsV1().Deployments(ns).Update(ctx, next, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}, FieldValidation: "Strict"})
	if err != nil {
		response.Fail(c, 12321, "集群配置校验失败："+err.Error())
		return
	}
	// Admission must not silently change read-only fields under this editor.
	checked := validated.DeepCopy()
	checked.ResourceVersion = dep.ResourceVersion
	if !sameReadOnlyConfiguration(next, checked) {
		response.Fail(c, 12321, "准入规则修改了只读配置，请通过集群配置流程处理")
		return
	}
	diffs = k8s.DeploymentConfigDifferences(dep, validated)
	rollout := !apiequality.Semantic.DeepEqual(dep.Spec.Template, validated.Spec.Template)
	replicasChanged := k8s.DesiredReplicas(dep) != k8s.DesiredReplicas(validated)
	if preview {
		response.OK(c, gin.H{"changes": diffs, "rolloutRequired": rollout, "replicasChanged": replicasChanged, "waitSeconds": int64(k8s.DeploymentWaitTimeout(validated) / time.Second), "progressDeadlineSeconds": validated.Spec.ProgressDeadlineSeconds})
		return
	}
	// Use the originally reviewed version; conflicting Jenkins/manual changes
	// are rejected by the Kubernetes API instead of overwritten.
	changesJSON, _ := json.Marshal(diffs)
	record := model.DeploymentConfigChange{Namespace: ns, DeploymentName: dep.Name, DeploymentUID: string(dep.UID), ResourceVersion: dep.ResourceVersion, Operator: middleware.GetCurrentUsername(c), ChangesJSON: string(changesJSON), Status: "prepared"}
	if err := repository.DB.Create(&record).Error; err != nil {
		response.Fail(c, 12321, "保存配置变更记录失败，尚未提交集群更新")
		return
	}
	next.ResourceVersion = req.ResourceVersion
	applied, err := cs.AppsV1().Deployments(ns).Update(ctx, next, metav1.UpdateOptions{FieldValidation: "Strict"})
	if err != nil {
		status := "unknown"
		if apierrors.IsConflict(err) || apierrors.IsInvalid(err) || apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
			status = "rejected"
		}
		_ = repository.DB.Model(&record).Updates(map[string]any{"status": status, "message": err.Error()}).Error
		response.Fail(c, 12321, "配置提交未完成，请重新加载核对："+err.Error())
		return
	}
	warning := ""
	if err := repository.DB.Model(&record).Updates(map[string]any{"status": "applied", "applied_generation": applied.Generation}).Error; err != nil {
		warning = "配置已提交，变更结果记录保存失败"
	}
	response.OKWithMessage(c, "配置已提交", gin.H{"deployment": applied, "changes": diffs, "rolloutRequired": rollout, "replicasChanged": replicasChanged, "waitSeconds": int64(k8s.DeploymentWaitTimeout(applied) / time.Second), "recordWarning": warning})
}

func sameReadOnlyConfiguration(expected, actual *appsv1.Deployment) bool {
	// CPU/memory defaults may be applied by LimitRange. All other fields,
	// including env, volumes, images, labels, and sidecars, must remain intact.
	a, b := expected.DeepCopy(), actual.DeepCopy()
	if len(a.Spec.Template.Spec.Containers) != len(b.Spec.Template.Spec.Containers) {
		return false
	}
	for i := range a.Spec.Template.Spec.Containers {
		for _, name := range []string{"cpu", "memory"} {
			delete(a.Spec.Template.Spec.Containers[i].Resources.Requests, corev1.ResourceName(name))
			delete(b.Spec.Template.Spec.Containers[i].Resources.Requests, corev1.ResourceName(name))
			delete(a.Spec.Template.Spec.Containers[i].Resources.Limits, corev1.ResourceName(name))
			delete(b.Spec.Template.Spec.Containers[i].Resources.Limits, corev1.ResourceName(name))
		}
		for _, container := range []*corev1.Container{&a.Spec.Template.Spec.Containers[i], &b.Spec.Template.Spec.Containers[i]} {
			if len(container.Resources.Requests) == 0 {
				container.Resources.Requests = nil
			}
			if len(container.Resources.Limits) == 0 {
				container.Resources.Limits = nil
			}
		}
	}
	return apiequality.Semantic.DeepEqual(a.Spec, b.Spec)
}
