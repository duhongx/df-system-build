package repository

import (
	"df-build-server/internal/model"
	"df-build-server/pkg/logger"

	"golang.org/x/crypto/bcrypt"
)

func AutoMigrate() error {
	err := DB.AutoMigrate(
		&model.User{},
		&model.Application{},
		&model.Task{},
		&model.Pipeline{},
		&model.PipelineStage{},
		&model.StageLog{},
		&model.Executor{},
		&model.BuildConfig{},
		&model.RemoteServer{},
		&model.Template{},
		&model.TemplateDefault{},
		&model.NotificationWebhook{},
		&model.Artifact{},
		&model.Settings{},
		&model.ConfigItem{},
		&model.Server{},
		&model.ServerLog{},
		&model.NotificationMsg{},
		&model.DownloadJob{},
		&model.ArtifactVersion{},
		&model.ArtifactVersionItem{},
		&model.ArtifactDeployBatch{},
		&model.ArtifactDeployRecord{},
		&model.DeploymentRuntimeVersion{},
		&model.SQLChangeBatch{},
		&model.SQLChangeFile{},
		&model.SQLChangeStatement{},
		&model.SQLViewBackup{},
		// Deployment-management models (replaces legacy DeployPlan/* set).
		&model.DeploymentSettings{},
		&model.DeploymentNetworkSettings{},
		&model.DeploymentEnvEntry{},
		&model.DeploymentEnabledComponent{},
		&model.DeploymentComponentTarget{},
		&model.DeploymentComponentState{},
		&model.DeploymentComponentOverride{},
		&model.Deployment{},
		&model.DeploymentRunLog{},
		&model.OfflineBundle{},
	)
	if err != nil {
		return err
	}

	dropLegacyDeployTables()

	logger.Log.Info("Database migration completed")
	return nil
}

// dropLegacyDeployTables removes the early "基础设施" stub tables that the
// deployment-management module replaces. Safe to run repeatedly.
func dropLegacyDeployTables() {
	for _, table := range []string{
		"deploy_plans",
		"deploy_executions",
		"deploy_logs",
		"environment_infos",
	} {
		if DB.Migrator().HasTable(table) {
			if err := DB.Migrator().DropTable(table); err != nil {
				logger.Log.Warnf("failed to drop legacy table %s: %v", table, err)
			} else {
				logger.Log.Infof("dropped legacy deploy table %s", table)
			}
		}
	}
}

// SeedCoreData seeds essential data on every startup (idempotent).
// It also detects first deployment and calls SeedFirstTimeData if needed.
func SeedCoreData() {
	// Check if first deployment (no users exist yet)
	var userCount int64
	DB.Model(&model.User{}).Count(&userCount)
	isFirstDeploy := (userCount == 0)

	// === Always: Admin user (only if "admin" doesn't exist) ===
	var adminUser model.User
	if DB.Where("username = ?", "admin").First(&adminUser).Error != nil {
		hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
		admin := model.User{
			Username:           "admin",
			PasswordHash:       string(hash),
			Email:              "admin@df-his.com",
			Phone:              "13800138000",
			Department:         "技术部",
			MustChangePassword: true,
		}
		DB.Create(&admin)
		logger.Log.Info("Default admin user created (admin/123456)")
	}

	// === Always: Settings keys (only insert missing) ===
	defaultSettings := []model.Settings{
		{Key: "concurrency_limit", Value: "5", Description: "全局并发上限"},
		{Key: "build_timeout_seconds", Value: "1800", Description: "构建超时时间(秒)"},
		{Key: "log_retention_days", Value: "30", Description: "日志保留天数"},
		{Key: "default_upload_path", Value: "/root/DFHIS/his-release", Description: "默认上传路径"},
		{Key: "clean_workspace_after_build", Value: "true", Description: "构建后清理工作区"},
		{Key: "deploy_source", Value: "both", Description: "部署来源: source(源码编译) / artifact(制品上传) / both(两者都显示)"},
		{Key: "build_mode", Value: "docker", Description: "编译模式: docker(容器编译) / local(本地编译)"},
		{Key: "deploy_mode", Value: "deploy", Description: "部署模式: deploy(编译+部署)"},
		{Key: "customer_env", Value: "本地", Description: "客户环境"},
		{Key: "docker_registry_url", Value: "192.168.199.102:8888", Description: "Docker 镜像仓库地址"},
		{Key: "docker_registry_user", Value: "admin", Description: "Docker 镜像仓库用户名"},
		{Key: "docker_registry_password", Value: "", Description: "Docker 镜像仓库密码"},
		{Key: "docker_registry_repo", Value: "cloudhis", Description: "Docker 镜像仓库名称(Nexus repository name)"},
		{Key: "k8s_kubeconfig_path", Value: "/root/.kube/config", Description: "K8s kubeconfig 文件路径"},
		{Key: "k8s_kubeconfig_content", Value: "", Description: "K8s kubeconfig 内容(手动录入时使用)"},
		{Key: "k8s_namespace", Value: "default", Description: "K8s 默认命名空间"},
		{Key: "skywalking_graphql_url", Value: "", Description: "SkyWalking GraphQL 地址 (如 http://192.168.1.154:28080/graphql)"},
		{Key: "nacos_url", Value: "", Description: "Nacos 地址 (如 http://192.168.199.101:8848/nacos)"},
		{Key: "nacos_user", Value: "nacos", Description: "Nacos 用户名"},
		{Key: "nacos_password", Value: "", Description: "Nacos 密码"},
		{Key: "skywalking_oap_url", Value: "", Description: "SkyWalking OAP 地址 (如 192.168.1.150:11800)"},
		{Key: "package_download_host", Value: "", Description: "软件包下载服务器地址，支持 host 或 host:port"},
		{Key: "package_download_user", Value: "root", Description: "软件包下载服务器用户名"},
		{Key: "package_download_password", Value: "", Description: "软件包下载服务器密码"},
		{Key: "package_download_key", Value: "", Description: "软件包下载服务器 SSH Key"},
		{Key: "package_download_path", Value: "/", Description: "软件包远程目录"},
		{Key: "postgresql_host", Value: "", Description: "PostgreSQL 主机地址"},
		{Key: "postgresql_port", Value: "5432", Description: "PostgreSQL 端口"},
		{Key: "postgresql_user", Value: "", Description: "PostgreSQL 用户名"},
		{Key: "postgresql_password", Value: "", Description: "PostgreSQL 密码"},
		{Key: "postgresql_admin_password", Value: "", Description: "PostgreSQL 管理员密码"},
		{Key: "postgresql_database", Value: "", Description: "PostgreSQL 数据库名"},
	}

	for _, s := range defaultSettings {
		var existing model.Settings
		if DB.Where(&model.Settings{Key: s.Key}).First(&existing).Error != nil {
			DB.Create(&s)
		}
	}

	// === Always: Core config items (only insert missing) ===
	SeedCoreConfigItems()

	// === First deploy only ===
	if isFirstDeploy {
		SeedFirstTimeData()
	}

	logger.Log.Info("Database seeding completed")
}

// SeedFirstTimeData seeds default data only on first deployment (when users table was empty).
func SeedFirstTimeData() {
	logger.Log.Info("First deployment detected, seeding default data...")

	// Build configs (7 default entries)
	defaultBuildConfigs := []model.BuildConfig{
		{Name: "Vue Yarn Docker", Category: "vue", BuildMode: "docker", DockerImage: "ops-builder-node:18-yarn1", CPULimit: "2", MemoryLimit: "4g", InstallCommand: "yarn install", BuildCommand: "npm run build:new", ArtifactDir: "dist", Status: "online", CacheMounts: `[{"hostPath":"/opt/build-cache/yarn","containerPath":"/usr/local/share/.cache/yarn"},{"hostPath":"/opt/build-cache/npm","containerPath":"/root/.npm"}]`},
		{Name: "Vue NPM Docker", Category: "vue", BuildMode: "docker", DockerImage: "ops-builder-node:18-npm", CPULimit: "2", MemoryLimit: "4g", InstallCommand: "npm install", BuildCommand: "npm run build", ArtifactDir: "dist", Status: "online", CacheMounts: `[{"hostPath":"/opt/build-cache/npm","containerPath":"/root/.npm"}]`},
		{Name: "Java Gradle Docker", Category: "java", BuildMode: "docker", DockerImage: "ops-builder-gradle:jdk8", CPULimit: "2", MemoryLimit: "4g", BuildCommand: "gradle clean build -x test", ArtifactDir: "build/libs", Status: "online", CacheMounts: `[{"hostPath":"/opt/build-cache/gradle","containerPath":"/root/.gradle/caches"},{"hostPath":"/opt/build-cache/maven","containerPath":"/root/.m2/repository"}]`},
		{Name: "Java Gradle JDK17 Docker", Category: "java", BuildMode: "docker", DockerImage: "ops-builder-gradle:jdk17", CPULimit: "2", MemoryLimit: "4g", BuildCommand: "gradle clean build -x test", ArtifactDir: "build/libs", Status: "online", CacheMounts: `[{"hostPath":"/opt/build-cache/gradle","containerPath":"/root/.gradle/caches"},{"hostPath":"/opt/build-cache/maven","containerPath":"/root/.m2/repository"}]`},
		{Name: "Java Maven Docker", Category: "java", BuildMode: "docker", DockerImage: "ops-builder-maven:jdk8", CPULimit: "2", MemoryLimit: "4g", BuildCommand: "mvn clean package -DskipTests", ArtifactDir: "target", Status: "online", CacheMounts: `[{"hostPath":"/opt/build-cache/maven","containerPath":"/root/.m2/repository"}]`},
		{Name: "本地 Gradle", Category: "java", BuildMode: "local", BuildCommand: "gradle clean build -x test", ArtifactDir: "build/libs", Status: "online"},
		{Name: "本地 Node", Category: "vue", BuildMode: "local", InstallCommand: "yarn install", BuildCommand: "npm run build:new", ArtifactDir: "dist", Status: "online"},
	}

	for _, bc := range defaultBuildConfigs {
		var existing model.BuildConfig
		if DB.Where("name = ?", bc.Name).First(&existing).Error != nil {
			DB.Create(&bc)
		}
	}

	// Personal config items (ConfigMaps, scripts)
	SeedPersonalConfigItems()

	// Seed default applications (104 apps from legacy config)
	SeedApplications()
}
