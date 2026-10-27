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
			state := frontendDeploymentFailureState(phase)
			runErr = fmt.Errorf("%w；前端发布进度=%s；线上状态=%s", runErr, phase, state)
			if result != nil {
				result.Error = runErr.Error()
				result.OnlineState = state
			}
		}
	}()
	fail := func(err error) (*types.StageResult, error) {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	checkpoint := func(next, image string) error {
		if err := service.RecordDeploymentProgress(pCtx.PipelineID, next, image); err != nil {
			return fmt.Errorf("记录发布进度失败: %w", err)
		}
		phase = next
		pCtx.Pipeline.DeploymentProgress = next
		return nil
	}
	b, err := loadWebBaseline(pCtx)
	if err != nil {
		return fail(err)
	}
	if len(b.Regions) == 0 {
		return fail(fmt.Errorf("缺少前端应用清单，请重新制作镜像"))
	}
	if err := validateWebBaseline(ctx, cs, pCtx, namespace); err != nil {
		return fail(err)
	}
	// Re-check disabled or reassigned registrations even after IMAGE_READY.
	if err := validateDeploymentRegistrations(b); err != nil {
		return fail(err)
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
		if err := verifyFrontendRollout(ctx, cs, b, b.PreparationImage, b.PreparedGeneration); err != nil {
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
		pCtx.OnLog(pCtx.PipelineID, 0, "资源准备镜像滚动完成，全部副本已就绪，开始更新正式镜像", "stdout")
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
	if err := verifyFrontendRollout(ctx, cs, b, pCtx.ImageName, b.FinalGeneration); err != nil {
		return fail(err)
	}
	phase = "completed"
	if err := checkpoint("completed", pCtx.ImageName); err != nil {
		return fail(err)
	}
	if err := service.CaptureAfterDeployVersions(ctx, pCtx.PipelineID, b.target(), pCtx.ImageName, reader, true); err != nil {
		return fail(fmt.Errorf("已发布并通过技术验证，保存结果异常: %w", err))
	}
	if err := savePublishedWebSite(ctx, cs, pCtx, b); err != nil {
		// The deployment already succeeded. An unavailable disk/cache must not
		// turn that fact into a failed business release; next build resyncs it.
		webLogger(pCtx).printf("正式发布成功，但本地完整站点未保存；下次构建将核对并重新同步：%v", err)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "前端镜像滚动更新完成：全部目标副本已运行指定镜像并就绪", "stdout")
	return &types.StageResult{}, nil
}

func frontendDeploymentFailureState(phase string) string {
	switch phase {
	case "precheck":
		return "本阶段尚未向 Kubernetes 提交修改，线上镜像未由本阶段改变"
	case "preparing":
		return "部署准备阶段，配置资源可能已创建；尚未提交 Deployment 镜像更新"
	case "assets_submitting":
		return "资源准备镜像的更新请求可能已被接受，请核对 Deployment 状态"
	case "assets_submitted", "assets_ready":
		return "资源准备镜像已提交，正式镜像尚未提交；不会自动撤销已提交的更新"
	case "submitting":
		return "正式镜像的更新请求可能已被接受，请核对 Deployment 状态"
	case "submitted", "verifying":
		return "正式镜像已提交，滚动更新结果尚未确认；不会自动撤销已提交的更新"
	case "completed":
		return "指定镜像已完成滚动更新且全部目标副本已就绪，后续记录保存失败"
	default:
		return "发布状态尚未确认，请核对 Deployment 的镜像和滚动状态"
	}
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
