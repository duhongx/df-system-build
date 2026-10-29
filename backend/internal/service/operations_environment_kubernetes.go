package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"df-build-server/internal/model"
	"gorm.io/gorm"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type inspectionKubeconfig struct {
	configured bool
	digest     string
	content    []byte
	path       string
	loadErr    error
}

// Read the settings and the referenced file once. Use these same bytes for the
// revision check and client creation, so a queued task cannot follow a new
// target (including replacement of a kubeconfig file at an unchanged path).
func environmentKubernetesSource(ctx context.Context, db *gorm.DB) (inspectionKubeconfig, error) {
	var settings []model.Settings
	if err := db.WithContext(ctx).Where("key IN ?", []string{"k8s_kubeconfig_path", "k8s_kubeconfig_content"}).Find(&settings).Error; err != nil {
		return inspectionKubeconfig{}, err
	}
	values := map[string]string{}
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	source := inspectionKubeconfig{}
	if strings.TrimSpace(values["k8s_kubeconfig_content"]) != "" {
		source.configured = true
		source.content = []byte(values["k8s_kubeconfig_content"])
	} else if strings.TrimSpace(values["k8s_kubeconfig_path"]) != "" {
		source.configured = true
		source.path = values["k8s_kubeconfig_path"]
		source.content, source.loadErr = os.ReadFile(source.path)
	}
	if source.configured {
		source.digest = fmt.Sprintf("%x", sha256.Sum256([]byte(mustJSON(struct {
			Settings map[string]string
			Content  []byte
			Readable bool
		}{values, source.content, source.loadErr == nil}))))
	}
	return source, nil
}

func (s inspectionKubeconfig) clientConfig() (*rest.Config, error) {
	if s.loadErr != nil {
		return nil, fmt.Errorf("读取环境 kubeconfig 失败，请核对文件路径和权限")
	}
	config, err := clientcmd.Load(s.content)
	if err != nil {
		return nil, err
	}
	if s.path != "" {
		for _, cluster := range config.Clusters {
			cluster.LocationOfOrigin = s.path
		}
		for _, user := range config.AuthInfos {
			user.LocationOfOrigin = s.path
		}
		for _, context := range config.Contexts {
			context.LocationOfOrigin = s.path
		}
		if err := clientcmd.ResolveLocalPaths(config); err != nil {
			return nil, err
		}
	}
	return clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
}
