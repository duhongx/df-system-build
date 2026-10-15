package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/pipeline/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

func verifyFrontendRollout(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, b *webBaseline, image, expectedHash string, preparation bool) error {
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return err
	}
	dep, err := readWebDeployment(ctx, cs, b.Namespace, b.target())
	if err != nil {
		return err
	}
	if dep == nil || webContainerImage(dep) != image {
		return fmt.Errorf("验证时目标镜像已改变")
	}
	pods, err := currentDeploymentPods(ctx, cs, b.Namespace, dep)
	if err != nil {
		return err
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas < 1 || len(pods) != int(replicas) {
		return fmt.Errorf("目标副本数量不完整，无法完成前端验证")
	}
	for _, pod := range pods {
		if _, ok := selectReadyPodForDeployment(dep, []corev1.Pod{pod}); !ok {
			return fmt.Errorf("Pod %s 未运行预期镜像或未 Ready", pod.Name)
		}
		tmp, err := os.MkdirTemp(pCtx.Workspace.PipelineDir(), ".web-verify-")
		if err != nil {
			return err
		}
		err = func() error {
			defer os.RemoveAll(tmp)
			if err := frontend.ExportPodSite(ctx, cs, cfg, b.Namespace, pod.Name, b.target(), tmp); err != nil {
				return err
			}
			actual, err := hashWebTree(tmp)
			if err != nil {
				return err
			}
			if actual != expectedHash {
				return fmt.Errorf("Pod %s 文件内容与构建清单不一致", pod.Name)
			}
			files, err := frontend.Inventory(tmp)
			if err != nil {
				return err
			}
			paths := map[string]bool{"index.html": true}
			// Retained files are checked by the full tree hash; also exercise old
			// resource URLs through the running Nginx in every replica.
			retainedHTTP := map[string]string{}
			for _, region := range b.Regions {
				for file := range region.Retained {
					if frontend.IsImmutable(file) {
						name := filepath.ToSlash(filepath.Join(region.Path, file))
						retainedHTTP[name] = files[name].SHA256
					}
				}
			}
			if err := frontend.VerifyHTTPFiles(ctx, cs, cfg, b.Namespace, pod.Name, b.target(), retainedHTTP); err != nil {
				return err
			}
			if !preparation {
				for _, region := range b.Regions {
					if region.Role == "cdn" {
						continue
					}
					refs, err := frontend.ValidateReferences(tmp, region.Path)
					if err != nil {
						return err
					}
					for _, ref := range refs {
						paths[ref] = true
					}
					entry := filepath.ToSlash(filepath.Join(region.Path, "index.html"))
					paths[entry] = true
					if region.Role == "sub" {
						body, _, err := frontend.ProbePodHTTP(ctx, cs, cfg, b.Namespace, pod.Name, b.target(), "/"+region.Path+"/__df_route_probe__")
						if err != nil {
							return err
						}
						sum := sha256.Sum256(body)
						if hex.EncodeToString(sum[:]) != files[entry].SHA256 {
							return fmt.Errorf("子应用 %s 的深层路由未返回自己的入口", region.Path)
						}
					}
				}
			}
			for file := range paths {
				body, headers, err := frontend.ProbePodHTTP(ctx, cs, cfg, b.Namespace, pod.Name, b.target(), "/"+file)
				if err != nil {
					return fmt.Errorf("HTTP 验证 /%s 失败: %w", file, err)
				}
				sum := sha256.Sum256(body)
				if hex.EncodeToString(sum[:]) != files[file].SHA256 {
					return fmt.Errorf("HTTP /%s 内容与清单不一致", file)
				}
				if frontend.IsImmutable(file) && strings.Contains(strings.ToLower(headers), "content-type: text/html") {
					return fmt.Errorf("静态资源 /%s 返回 HTML", file)
				}
			}
			_, headers, probeErr := frontend.ProbePodHTTP(ctx, cs, cfg, b.Namespace, pod.Name, b.target(), "/__df_missing_resource_"+fmt.Sprint(pCtx.PipelineID)+".js")
			if probeErr == nil || frontend.HTTPStatus(headers) != 404 {
				return fmt.Errorf("缺失 JS 必须返回 404，当前路由配置不符合发布规则")
			}
			return nil
		}()
		if err != nil {
			return err
		}
		pCtx.OnLog(pCtx.PipelineID, 0, "Pod "+pod.Name+" 全站摘要和 HTTP 验证通过", "stdout")
	}
	after, err := readWebDeployment(ctx, cs, b.Namespace, b.target())
	if err != nil {
		return err
	}
	if after == nil || after.UID != dep.UID || after.Generation != dep.Generation || webContainerImage(after) != image {
		return fmt.Errorf("验证期间 Deployment 已改变")
	}
	return nil
}

func validateFrontendRoutingBefore(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, b *webBaseline) error {
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return err
	}
	dep, err := readWebDeployment(ctx, cs, b.Namespace, b.target())
	if err != nil {
		return err
	}
	if dep == nil {
		return fmt.Errorf("Deployment 不存在")
	}
	pods, err := currentDeploymentPods(ctx, cs, b.Namespace, dep)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return fmt.Errorf("没有可验证的副本")
	}
	for _, pod := range pods {
		if _, ok := selectReadyPodForDeployment(dep, []corev1.Pod{pod}); !ok {
			return fmt.Errorf("Pod %s 未 Ready", pod.Name)
		}
		_, headers, probeErr := frontend.ProbePodHTTP(ctx, cs, cfg, b.Namespace, pod.Name, b.target(), "/__df_missing_resource_"+fmt.Sprint(pCtx.PipelineID)+".js")
		if probeErr == nil || frontend.HTTPStatus(headers) != 404 {
			return fmt.Errorf("Pod %s 缺失 JS 未返回 404", pod.Name)
		}
		if strings.Contains(strings.ToLower(headers), "max-age=604800") {
			return fmt.Errorf("Pod %s 为静态 404 设置了七天缓存", pod.Name)
		}
		for _, region := range b.Regions {
			if region.Role != "sub" {
				continue
			}
			body, headers, probeErr := frontend.ProbePodHTTP(ctx, cs, cfg, b.Namespace, pod.Name, b.target(), "/"+region.Path+"/__df_route_probe__")
			old, exists := region.Before["index.html"]
			if !exists {
				if probeErr == nil || frontend.HTTPStatus(headers) != 404 {
					return fmt.Errorf("Pod %s 新子应用路径错误地回退到其他入口", pod.Name)
				}
				continue
			}
			if probeErr != nil {
				return fmt.Errorf("Pod %s 子应用深层路由不可访问", pod.Name)
			}
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != old.SHA256 {
				return fmt.Errorf("Pod %s 子应用 %s 深层路由返回了其他入口", pod.Name, region.Path)
			}
		}
	}
	return nil
}
