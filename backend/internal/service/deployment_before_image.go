package service

import (
	"context"
	"errors"
	"fmt"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Desired is what the previous update submitted; Rollback is the image we can
// restore. A failed rollout must not replace the previously recorded baseline.
type BeforeDeploymentImages struct{ Desired, Rollback string }

func ImagesBeforeDeployment(ctx context.Context, namespace, name string, pipelineID uint) (BeforeDeploymentImages, error) {
	cs, err := k8s.GetClient()
	if err != nil {
		return BeforeDeploymentImages{}, err
	}
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return BeforeDeploymentImages{}, err
	}
	return resolveBeforeDeploymentImages(dep, namespace, name, pipelineID)
}

func resolveBeforeDeploymentImages(dep *appsv1.Deployment, namespace, name string, pipelineID uint) (BeforeDeploymentImages, error) {
	images := BeforeDeploymentImages{Desired: DeploymentContainerImage(dep, name)}
	if images.Desired == "" {
		return images, fmt.Errorf("Deployment %s/%s 缺少目标镜像", namespace, name)
	}
	if deploymentRolloutComplete(dep) {
		images.Rollback = images.Desired
		return images, nil
	}
	// Match the currently submitted image, rather than choosing an unrelated
	// historical update. Failed retries carry the same recoverable BeforeImage.
	var previous model.ArtifactDeployRecord
	err := repository.DB.Where("namespace = ? AND deployment_name = ? AND after_image = ? AND pipeline_id <> ?", namespace, name, images.Desired, pipelineID).Order("id DESC").First(&previous).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return images, nil
	}
	if err != nil {
		return images, fmt.Errorf("读取 %s/%s 的回滚镜像记录失败: %w", namespace, name, err)
	}
	if previous.DeployStatus == "success" {
		images.Rollback = images.Desired
	} else {
		images.Rollback = previous.BeforeImage
	}
	return images, nil
}

func deploymentRolloutComplete(dep *appsv1.Deployment) bool {
	if dep == nil {
		return false
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	return replicas > 0 && dep.Status.ObservedGeneration >= dep.Generation && dep.Status.Replicas == replicas && dep.Status.UpdatedReplicas == replicas && dep.Status.ReadyReplicas == replicas && dep.Status.AvailableReplicas == replicas && dep.Status.UnavailableReplicas == 0
}
