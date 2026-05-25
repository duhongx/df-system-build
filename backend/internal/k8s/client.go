package k8s

import (
	"context"
	"fmt"
	"sync"

	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	clientOnce sync.Once
	clientSet  *kubernetes.Clientset
	restConfig *rest.Config
	initErr    error
)

// GetClient returns the shared K8s clientset (lazy init)
func GetClient() (*kubernetes.Clientset, error) {
	clientOnce.Do(func() {
		clientSet, restConfig, initErr = buildClient()
	})
	if initErr != nil {
		// Reset once so next call retries
		clientOnce = sync.Once{}
		return nil, initErr
	}
	return clientSet, nil
}

// GetRestConfig returns the rest config for advanced operations (exec, cp)
func GetRestConfig() (*rest.Config, error) {
	if _, err := GetClient(); err != nil {
		return nil, err
	}
	return restConfig, nil
}

// ResetClient forces re-initialization (call after kubeconfig changes)
func ResetClient() {
	clientOnce = sync.Once{}
	clientSet = nil
	restConfig = nil
	initErr = nil
}

func buildClient() (*kubernetes.Clientset, *rest.Config, error) {
	settingsRepo := repository.NewSettingsRepo()
	kubeconfigContent, _ := settingsRepo.GetByKey("k8s_kubeconfig_content")
	kubeconfigPath, _ := settingsRepo.GetByKey("k8s_kubeconfig_path")
	return buildClientFromSettings(map[string]string{
		"k8s_kubeconfig_content": kubeconfigContent,
		"k8s_kubeconfig_path":    kubeconfigPath,
	})
}

func buildClientFromSettings(settings map[string]string) (*kubernetes.Clientset, *rest.Config, error) {
	// Try kubeconfig content first (manual input)
	kubeconfigContent := settings["k8s_kubeconfig_content"]
	if kubeconfigContent != "" {
		cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigContent))
		if err != nil {
			return nil, nil, fmt.Errorf("解析 kubeconfig 内容失败: %w", err)
		}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("创建 K8s 客户端失败: %w", err)
		}
		logger.Log.Info("K8s client initialized from kubeconfig content")
		return cs, cfg, nil
	}

	// Fall back to kubeconfig file path
	kubeconfigPath := settings["k8s_kubeconfig_path"]
	if kubeconfigPath == "" {
		kubeconfigPath = "/root/.kube/config"
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("加载 kubeconfig 文件失败 (%s): %w", kubeconfigPath, err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("创建 K8s 客户端失败: %w", err)
	}

	logger.Log.Infof("K8s client initialized from %s", kubeconfigPath)
	return cs, cfg, nil
}

// GetDefaultNamespace returns the configured default namespace
func GetDefaultNamespace() string {
	settingsRepo := repository.NewSettingsRepo()
	ns, _ := settingsRepo.GetByKey("k8s_namespace")
	if ns == "" {
		return "default"
	}
	return ns
}

// ListNamespaces returns all namespace names
func ListNamespaces(ctx context.Context) ([]string, error) {
	cs, err := GetClient()
	if err != nil {
		return nil, err
	}
	return listNamespaces(ctx, cs)
}

func ListNamespacesWithSettings(ctx context.Context, settings map[string]string) ([]string, error) {
	if settings == nil {
		return ListNamespaces(ctx)
	}
	cs, _, err := buildClientFromSettings(settings)
	if err != nil {
		return nil, err
	}
	return listNamespaces(ctx, cs)
}

func listNamespaces(ctx context.Context, cs *kubernetes.Clientset) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	nsList, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	names := make([]string, len(nsList.Items))
	for i, ns := range nsList.Items {
		names[i] = ns.Name
	}
	return names, nil
}

// GetFirstNodeIP returns the InternalIP of the first Node
func GetFirstNodeIP(ctx context.Context) string {
	cs, err := GetClient()
	if err != nil {
		return ""
	}

	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		return ""
	}

	for _, addr := range nodes.Items[0].Status.Addresses {
		if addr.Type == "InternalIP" {
			return addr.Address
		}
	}
	return ""
}
