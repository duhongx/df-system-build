package service

import (
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"strings"
	"testing"
)

func TestTaskAutomaticallyUsesGradleAndNodeDefaults(t *testing.T) {
	setupTaskServiceTestDB(t)
	settings := repository.NewSettingsRepo()
	for key, value := range map[string]string{"customer_env": "本地", "deploy_source": "both"} {
		if err := settings.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	java := model.Application{AppName: "default-java", AppType: "java", GitRepo: "git@example:java.git"}
	web := model.Application{AppName: "default-web", AppType: "vue", GitRepo: "git@example:web.git"}
	for _, app := range []*model.Application{&java, &web} {
		if err := repository.DB.Create(app).Error; err != nil {
			t.Fatal(err)
		}
	}
	gradle := model.BuildConfig{Name: "shared-gradle", Category: "java", BuildCommand: "gradle clean build -x test", Status: "online"}
	preferred := model.BuildConfig{Name: "existing-toolchain", Category: "java", BuildCommand: "/tools/gradle/bin/gradle clean build -x test", EnvVars: "JAVA_HOME=/tools/jdk8", Status: "online"}
	node := model.BuildConfig{Name: "shared-node", Category: "vue", InstallCommand: "yarn install", BuildCommand: "npm run build:new", Status: "online"}
	offline := model.BuildConfig{Name: "offline-gradle", Category: "java", BuildCommand: "gradle build", Status: "offline"}
	maven := model.BuildConfig{Name: "maven", Category: "java", BuildCommand: "mvn package", Status: "online"}
	for _, bc := range []*model.BuildConfig{&gradle, &preferred, &node, &offline, &maven} {
		if err := repository.DB.Create(bc).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []*model.Task{{TaskName: "existing-gradle", ApplicationID: java.ID, BuildConfigID: preferred.ID}, {TaskName: "existing-maven-1", ApplicationID: java.ID, BuildConfigID: maven.ID}, {TaskName: "existing-maven-2", ApplicationID: java.ID, BuildConfigID: maven.ID}} {
		if err := repository.DB.Create(task).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewTaskService()
	// Neither a configuration ID nor a per-task tool choice is required.
	task, err := service.Create(&CreateTaskRequest{TaskName: "auto-java", ApplicationID: java.ID, GitBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if task.BuildConfigID != preferred.ID || task.BuildConfig.EnvVars != preferred.EnvVars {
		t.Fatalf("existing Gradle toolchain not reused: %+v", task)
	}
	task, err = service.Update(task.ID, &UpdateTaskRequest{ApplicationID: web.ID})
	if err != nil {
		t.Fatal(err)
	}
	if task.BuildConfigID != node.ID {
		t.Fatalf("changing application did not switch to Node: %+v", task)
	}
	created, err := service.Create(&CreateTaskRequest{TaskName: "auto-web", ApplicationID: web.ID, GitBranch: "main", BuildConfigID: gradle.ID})
	if err != nil || created.BuildConfigID != node.ID {
		t.Fatalf("legacy mismatched choice not ignored: %+v %v", created, err)
	}
	if err := repository.DB.Model(&model.BuildConfig{}).Where("id = ?", node.ID).Update("status", "offline").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(&CreateTaskRequest{TaskName: "disabled-web", ApplicationID: web.ID, GitBranch: "main"}); err == nil {
		t.Fatal("disabled toolchain should fail")
	}
	for _, env := range []string{"浙江", "新疆", "local"} {
		if err := settings.Set("customer_env", env); err != nil {
			t.Fatal(err)
		}
		if err := repository.RequireSourceBuild(); err == nil || !strings.Contains(err.Error(), "非本地") {
			t.Fatalf("%s should reject source builds: %v", env, err)
		}
	}
}
