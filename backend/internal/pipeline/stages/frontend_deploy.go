package stages

import (
	"context"
	"fmt"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func (s *K8sDeployStage) runFrontendDeployment(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, namespace string) (result *types.StageResult, runErr error) {
	phase := "precheck"
	defer func() {
		if runErr != nil {
			runErr = fmt.Errorf("%w；前端发布进度=%s（已接受的集群操作不会自动撤销）", runErr, phase)
			if result != nil {
				result.Error = runErr.Error()
			}
		}
	}()
	fail := func(err error) (*types.StageResult, error) {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	checkpoint := func(next, image string) error {
		phase = next
		pCtx.Pipeline.DeploymentProgress = next
		if err := service.RecordDeploymentProgress(pCtx.PipelineID, next, image); err != nil {
			return fmt.Errorf("记录发布进度失败: %w", err)
		}
		return nil
	}
	b, err := loadWebBaseline(pCtx)
	if err != nil {
		return fail(err)
	}
	if b.FinalSHA256 == "" || len(b.Regions) == 0 {
		return fail(fmt.Errorf("缺少前端文件清单，请重新制作镜像"))
	}
	if err := validateWebBaseline(ctx, cs, pCtx, namespace); err != nil {
		return fail(err)
	}
	// Re-check disabled or reassigned registrations even after IMAGE_READY.
	if err := validateDeploymentRegistrations(b); err != nil {
		return fail(err)
	}
	if b.Exists {
		if err := validateFrontendRoutingBefore(ctx, cs, pCtx, b); err != nil {
			return fail(fmt.Errorf("现网路由预检查失败，尚未提交镜像更新；请先通过配置变更修复 Nginx: %w", err))
		}
	}
	reader := service.NewK8sRuntimeVersionReader()
	before, err := service.ImagesBeforeDeployment(ctx, namespace, b.target(), pCtx.PipelineID)
	if err == nil {
		if err := service.CaptureBeforeDeployImages(ctx, pCtx.PipelineID, b.target(), before, reader); err != nil {
			return fail(err)
		}
	} else if b.Exists {
		return fail(err)
	}
	if err := checkpoint("preparing", pCtx.ImageName); err != nil {
		return fail(err)
	}
	// Existing sites only change images. ConfigMap, Service, Ingress, probes,
	// mounts, and all other spec fields are untouched by routine releases.
	if !b.Exists {
		plan := resolvePlan(pCtx)
		if plan.configMapCode != "" {
			code := plan.configMapCode
			if pCtx.VueRole == "standalone" {
				code = "configmap-" + pCtx.AppName
			}
			if err := s.applyConfigMap(ctx, cs, pCtx, namespace, code); err != nil {
				return fail(err)
			}
		}
		if plan.serviceCode != "" {
			if err := s.applyService(ctx, cs, pCtx, namespace, b.target(), plan.serviceCode); err != nil {
				return fail(err)
			}
		}
		for _, ing := range pCtx.Ingresses {
			if ing.Host != "" {
				name := ing.Name
				if name == "" {
					name = b.target()
				}
				if err := s.applyIngress(ctx, cs, pCtx, namespace, b.target(), name, ing.Host); err != nil {
					return fail(err)
				}
			}
		}
	}
	if b.PreparationImage != "" {
		if err := checkpoint("assets_submitting", b.PreparationImage); err != nil {
			return fail(err)
		}
		dep, err := updateFrontendImage(ctx, cs, b, b.PreparationImage)
		if err != nil {
			return fail(err)
		}
		phase = "assets_submitted"
		b.PreparedGeneration = dep.Generation
		if err := saveWebBaseline(pCtx, b); err != nil {
			return fail(err)
		}
		if err := repository.NewPipelineRepo().SaveImageCheckpoint(pCtx.Pipeline); err != nil {
			return fail(fmt.Errorf("资源镜像已提交，保存阶段失败: %w", err))
		}
		if err := checkpoint("assets_submitted", b.PreparationImage); err != nil {
			return fail(err)
		}
		targetImage := pCtx.ImageName
		pCtx.ImageName = b.PreparationImage
		err = s.waitForRollout(ctx, cs, namespace, b.target(), pCtx)
		pCtx.ImageName = targetImage
		if err != nil {
			return fail(err)
		}
		if err := verifyFrontendRollout(ctx, cs, pCtx, b, b.PreparationImage, b.PreparationSHA256, true); err != nil {
			return fail(err)
		}
		b.AssetsVerified = true
		if err := saveWebBaseline(pCtx, b); err != nil {
			return fail(err)
		}
		if err := repository.NewPipelineRepo().SaveImageCheckpoint(pCtx.Pipeline); err != nil {
			return fail(err)
		}
		if err := checkpoint("assets_ready", b.PreparationImage); err != nil {
			return fail(err)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, "全部副本已具备新旧静态资源，开始切换所选入口", "stdout")
	}
	if err := validateWebBaseline(ctx, cs, pCtx, namespace); err != nil {
		return fail(err)
	}
	if err := checkpoint("submitting", pCtx.ImageName); err != nil {
		return fail(err)
	}
	if b.Exists {
		var submitted *appsv1.Deployment
		submitted, err = updateFrontendImage(ctx, cs, b, pCtx.ImageName)
		if err == nil {
			b.FinalGeneration = submitted.Generation
		}
	} else {
		err = s.applyDeployment(ctx, cs, pCtx, namespace, b.target(), "deployment-web")
	}
	if err != nil {
		return fail(err)
	}
	phase = "submitted"
	if err := saveWebBaseline(pCtx, b); err != nil {
		return fail(err)
	}
	if err := repository.NewPipelineRepo().SaveImageCheckpoint(pCtx.Pipeline); err != nil {
		return fail(err)
	}
	if err := checkpoint("submitted", pCtx.ImageName); err != nil {
		return fail(err)
	}
	if err := s.waitForRollout(ctx, cs, namespace, b.target(), pCtx); err != nil {
		return fail(err)
	}
	phase = "verifying"
	if err := checkpoint("verifying", pCtx.ImageName); err != nil {
		return fail(err)
	}
	if err := verifyFrontendRollout(ctx, cs, pCtx, b, pCtx.ImageName, b.FinalSHA256, false); err != nil {
		return fail(err)
	}
	phase = "completed"
	if err := checkpoint("completed", pCtx.ImageName); err != nil {
		return fail(err)
	}
	if err := service.CaptureAfterDeployVersions(ctx, pCtx.PipelineID, b.target(), pCtx.ImageName, reader, true); err != nil {
		return fail(fmt.Errorf("已发布并通过技术验证，保存结果异常: %w", err))
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "前端更新完成：全部副本文件、入口、静态资源和路由验证通过；业务菜单及权限按业务接入配置验收", "stdout")
	return &types.StageResult{}, nil
}

func updateFrontendImage(ctx context.Context, cs kubernetes.Interface, b *webBaseline, image string) (*appsv1.Deployment, error) {
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return nil, err
	}
	dep, err := readWebDeployment(ctx, cs, b.Namespace, b.target())
	if err != nil {
		return nil, err
	}
	if err := b.validate(dep, cfg.Host, b.Namespace); err != nil {
		return nil, err
	}
	if dep == nil {
		return nil, fmt.Errorf("目标 Deployment 不存在")
	}
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == b.target() {
			dep.Spec.Template.Spec.Containers[i].Image = image
		}
	}
	if dep.Annotations == nil {
		dep.Annotations = map[string]string{}
	}
	dep.Annotations["df-build-system/pipeline"] = fmt.Sprint(b.PipelineID)
	return cs.AppsV1().Deployments(b.Namespace).Update(ctx, dep, metav1.UpdateOptions{})
}

func validateDeploymentRegistrations(b *webBaseline) error {
	apps, err := repository.NewApplicationRepo().ListAll()
	if err != nil {
		return err
	}
	byName := map[string]model.Application{}
	for _, app := range apps {
		byName[app.AppName] = app
	}
	for _, region := range b.Regions {
		app := byName[region.AppName]
		if app.ID != region.AppID || !app.Enabled || app.VueRole != region.Role || app.DeploymentTarget() != b.target() || (region.Role == "sub" && app.AppCode != region.AppCode) {
			return fmt.Errorf("应用 %s 已停用或部署身份已变化", region.AppName)
		}
		if err := app.ValidateIdentity(); err != nil {
			return err
		}
	}
	return nil
}
