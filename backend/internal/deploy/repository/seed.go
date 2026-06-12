package repository

import (
	"encoding/json"
	"time"

	"df-build-server/internal/deploy/defaults"
	"df-build-server/internal/model"

	"gorm.io/gorm"
)

// defaultEnabledComponents mirrors his-deploy's canonical full-stack lineup so
// a fresh database lands on a populated component list instead of an empty page.
var defaultEnabledComponents = []string{
	"check", "preflight", "nexus", "docker", "controller-render",
	"prepare", "nfs", "slb", "etcd", "containerd", "kube-lb",
	"master", "node", "calico", "postgresql", "elasticsearch",
	"redis", "rabbitmq", "minio", "plugin", "ftp", "dns",
	"skywalking", "df-ops", "nacos",
}

// SeedDeploymentDefaults primes the deployment-management tables on startup.
// It is idempotent: singleton rows and component-default overrides are only
// inserted when missing, so operator edits are never clobbered.
//
// Critically, it seeds component_overrides from the embedded defaults package
// (redis password, docker data_root, postgresql data_dir, ...). Without this,
// the render phase would emit ${redis.password} and friends as literal text
// into generated config — matching his-deploy's seedComponentDefaults contract.
func SeedDeploymentDefaults(db *gorm.DB) error {
	now := time.Now()

	// Singleton deployment settings.
	var depCount int64
	db.Model(&model.DeploymentSettings{}).Count(&depCount)
	if depCount == 0 {
		if err := db.Create(&model.DeploymentSettings{
			ID: 1, SSHUser: "root", RemoteRoot: "/opt/his-deploy",
			SSHPort: 22, RetainDeployments: 100, DefaultTimeoutSeconds: 1800, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
	}

	// Singleton network settings.
	var netCount int64
	db.Model(&model.DeploymentNetworkSettings{}).Count(&netCount)
	if netCount == 0 {
		if err := db.Create(&model.DeploymentNetworkSettings{
			ID: 1, ServiceCIDR: "10.96.0.0/12", ClusterCIDR: "10.244.0.0/16",
			NodeCIDRMaskSize: 24, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
	}

	// Default env entry (only on first install).
	var envCount int64
	db.Model(&model.DeploymentEnvEntry{}).Count(&envCount)
	if envCount == 0 {
		if err := db.Create(&model.DeploymentEnvEntry{Key: "name", Value: "prod", UpdatedAt: now}).Error; err != nil {
			return err
		}
	}

	// Default enabled components (only when empty).
	var ecCount int64
	db.Model(&model.DeploymentEnabledComponent{}).Count(&ecCount)
	if ecCount == 0 {
		for i, name := range defaultEnabledComponents {
			if err := db.Create(&model.DeploymentEnabledComponent{Name: name, Position: i, UpdatedAt: now}).Error; err != nil {
				return err
			}
		}
	}

	// Component default overrides — insert only missing component names.
	all, err := defaults.All()
	if err != nil {
		return err
	}
	for name, params := range all {
		var n int64
		db.Model(&model.DeploymentComponentOverride{}).Where("component_name = ?", name).Count(&n)
		if n > 0 {
			continue
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		if err := db.Create(&model.DeploymentComponentOverride{
			ComponentName: name, ParamsJSON: string(raw), UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}
