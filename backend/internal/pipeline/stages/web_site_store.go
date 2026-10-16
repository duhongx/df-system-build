package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// A site belongs to one actual runtime, not to a source branch or build task.
// Image IDs also invalidate a cache when a mutable tag is reused on new Pods.
type webSiteIdentity struct {
	Cluster    string            `json:"cluster"`
	Namespace  string            `json:"namespace"`
	Target     string            `json:"target"`
	UID        string            `json:"uid"`
	Generation int64             `json:"generation"`
	Image      string            `json:"image"`
	ConfigMaps map[string]string `json:"configMaps"`
	ImageIDs   []string          `json:"imageIds"`
}

type webSiteSnapshot struct {
	Schema   int             `json:"schema"`
	Snapshot string          `json:"snapshot"`
	Identity webSiteIdentity `json:"identity"`
	SHA256   string          `json:"sha256"`
}

type webSiteStore struct{ root string }

func newWebSiteStore(base string, identity webSiteIdentity) (*webSiteStore, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("本地前端完整目录缺少工作区根路径")
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	// Do not interpolate environment or application names into filesystem paths.
	key := sha256.Sum256([]byte(identity.Cluster + "\x00" + identity.Namespace + "\x00" + identity.Target))
	return &webSiteStore{root: filepath.Join(abs, "frontend-sites", hex.EncodeToString(key[:]))}, nil
}

func validSiteSnapshot(name string) bool {
	return filepath.Base(name) == name && strings.HasPrefix(name, "site-") && !strings.ContainsAny(name, "\\\r\n")
}

func (s *webSiteStore) read() (*webSiteSnapshot, error) {
	data, err := os.ReadFile(filepath.Join(s.root, "current.json"))
	if err != nil {
		return nil, err
	}
	var state webSiteSnapshot
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Schema != 1 || !validSiteSnapshot(state.Snapshot) || len(state.SHA256) != 64 {
		return nil, fmt.Errorf("本地前端完整目录清单无效")
	}
	return &state, nil
}

// materialize never returns an unverified directory or modifies the saved site.
func (s *webSiteStore) materialize(identity webSiteIdentity, destination string) (string, error) {
	state, err := s.read()
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(state.Identity, identity) {
		return "", fmt.Errorf("线上前端身份或版本已改变，需要重新同步 Pod")
	}
	source := filepath.Join(s.root, state.Snapshot, "html")
	if err := copyDirContents(source, destination); err != nil {
		return "", err
	}
	hash, err := hashWebTree(destination)
	if err != nil {
		return "", err
	}
	if hash != state.SHA256 {
		return "", fmt.Errorf("本地前端完整目录内容已改变，需要重新同步 Pod")
	}
	return hash, nil
}

// save installs a complete immutable snapshot, then atomically switches the
// pointer. A failed copy, checksum, or runtime recheck leaves the old site intact.
func (s *webSiteStore) save(source string, identity webSiteIdentity, expected string, recheck func() error) error {
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	files, err := frontend.Inventory(source)
	if err != nil {
		return err
	}
	var bytes uint64
	for _, file := range files {
		bytes += uint64(file.Size)
	}
	if err := frontend.RequireSpace(s.root, bytes); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(s.root, "site-")
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := copyDirContents(source, filepath.Join(staging, "html")); err != nil {
		return err
	}
	hash, err := hashWebTree(filepath.Join(staging, "html"))
	if err != nil {
		return err
	}
	if hash != expected {
		return fmt.Errorf("待保存的完整站点与已验证文件清单不一致")
	}
	data, err := json.MarshalIndent(webSiteSnapshot{Schema: 1, Snapshot: filepath.Base(staging), Identity: identity, SHA256: hash}, "", "  ")
	if err != nil {
		return err
	}
	pointer, err := os.CreateTemp(s.root, ".current-")
	if err != nil {
		return err
	}
	defer os.Remove(pointer.Name())
	if _, err = pointer.Write(data); err == nil {
		err = pointer.Sync()
	}
	closeErr := pointer.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if recheck != nil {
		if err := recheck(); err != nil {
			return err
		}
	}
	previous, _ := s.read()
	if err := os.Rename(pointer.Name(), filepath.Join(s.root, "current.json")); err != nil {
		return err
	}
	installed = true
	if previous != nil && previous.Snapshot != filepath.Base(staging) {
		_ = os.RemoveAll(filepath.Join(s.root, previous.Snapshot))
	}
	return nil
}

func localWebSiteEnabled() (bool, error) {
	environment, err := repository.NewSettingsRepo().Read("customer_env", false)
	return strings.TrimSpace(environment) == "本地", err
}

func currentWebSite(ctx context.Context, cs kubernetes.Interface, cluster, namespace, target string) (*webBaseline, webSiteIdentity, error) {
	identity := webSiteIdentity{Cluster: cluster, Namespace: namespace, Target: target, ConfigMaps: map[string]string{}}
	dep, err := readWebDeployment(ctx, cs, namespace, target)
	if err != nil {
		return nil, identity, err
	}
	b := &webBaseline{Exists: dep != nil, Cluster: cluster, Namespace: namespace, DeploymentName: target, ConfigMaps: map[string]string{}}
	if dep == nil {
		return b, identity, nil
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas < 1 || dep.Status.ObservedGeneration < dep.Generation || dep.Status.Replicas != replicas || dep.Status.UpdatedReplicas != replicas || dep.Status.ReadyReplicas != replicas || dep.Status.AvailableReplicas != replicas || dep.Status.UnavailableReplicas != 0 {
		return nil, identity, fmt.Errorf("%s 尚未完成当前版本滚动更新，不能使用本地完整目录", target)
	}
	pods, err := currentDeploymentPods(ctx, cs, namespace, dep)
	if err != nil {
		return nil, identity, err
	}
	if len(pods) != int(replicas) {
		return nil, identity, fmt.Errorf("%s 当前副本数量不完整", target)
	}
	imageIDs := map[string]bool{}
	for _, pod := range pods {
		if _, ok := selectReadyPodForDeployment(dep, []corev1.Pod{pod}); !ok {
			return nil, identity, fmt.Errorf("%s 存在非 Ready 或镜像不一致的副本", target)
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == target {
				if status.ImageID == "" {
					return nil, identity, fmt.Errorf("%s 副本缺少实际镜像 ID", target)
				}
				imageIDs[status.ImageID] = true
			}
		}
	}
	if len(imageIDs) != 1 {
		return nil, identity, fmt.Errorf("%s 副本的实际镜像 ID 不一致", target)
	}
	for id := range imageIDs {
		identity.ImageIDs = append(identity.ImageIDs, id)
	}
	sort.Strings(identity.ImageIDs)
	for _, volume := range dep.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil {
			cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, volume.ConfigMap.Name, metav1.GetOptions{})
			if err != nil {
				return nil, identity, err
			}
			identity.ConfigMaps[cm.Name] = cm.ResourceVersion
		}
	}
	identity.UID, identity.Generation, identity.Image = string(dep.UID), dep.Generation, webContainerImage(dep)
	b.UID, b.Generation, b.Image, b.ResourceVersion, b.ConfigMaps = identity.UID, identity.Generation, identity.Image, dep.ResourceVersion, identity.ConfigMaps
	return b, identity, nil
}

func webSiteRecheck(ctx context.Context, cs kubernetes.Interface, identity webSiteIdentity) error {
	b, current, err := currentWebSite(ctx, cs, identity.Cluster, identity.Namespace, identity.Target)
	if err != nil {
		return err
	}
	if !b.Exists || !reflect.DeepEqual(current, identity) {
		return fmt.Errorf("同步完整目录期间线上前端已变化，放弃保存或复用")
	}
	return nil
}

func (s *BuildImageStage) frontendBaseline(ctx context.Context, pCtx *types.PipelineContext, namespace, destination, target string) (*webBaseline, error) {
	local, err := localWebSiteEnabled()
	if err != nil {
		return nil, err
	}
	if !local {
		return s.copyFromPod(ctx, pCtx, namespace, destination, target)
	}
	cs, err := k8s.GetClient()
	if err != nil {
		return nil, err
	}
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return nil, err
	}
	b, identity, err := currentWebSite(ctx, cs, cfg.Host, namespace, target)
	if err != nil || !b.Exists {
		return b, err
	}
	store, err := newWebSiteStore(pCtx.Workspace.BaseDir, identity)
	if err != nil {
		return nil, err
	}
	log := webLogger(pCtx)
	hash, err := store.materialize(identity, destination)
	if err == nil {
		if err := webSiteRecheck(ctx, cs, identity); err != nil {
			return nil, err
		}
		b.ContentSHA256 = hash
		log.printf("复用本地完整站点；Deployment=%s/%s；目录=%s；SHA256=%s", namespace, target, store.root, hash)
		return b, nil
	}
	log.printf("本地完整站点不可复用，重新从 Pod 同步；Deployment=%s/%s；原因=%v", namespace, target, err)
	if err := os.RemoveAll(destination); err != nil {
		return nil, err
	}
	b, err = s.copyFromPod(ctx, pCtx, namespace, destination, target)
	if err != nil {
		return nil, err
	}
	if !b.Exists || b.UID != identity.UID || b.Generation != identity.Generation || b.Image != identity.Image {
		return nil, fmt.Errorf("同步期间前端基线变化，请重新执行")
	}
	if err := store.save(destination, identity, b.ContentSHA256, func() error { return webSiteRecheck(ctx, cs, identity) }); err != nil {
		return nil, fmt.Errorf("初始化本地完整站点失败: %w", err)
	}
	log.printf("已同步本地完整站点；Deployment=%s/%s；目录=%s", namespace, target, store.root)
	return b, nil
}

// Invoked only after the final rollout and all replicas passed verification.
func savePublishedWebSite(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, published *webBaseline) error {
	local, err := localWebSiteEnabled()
	if err != nil || !local {
		return err
	}
	b, identity, err := currentWebSite(ctx, cs, published.Cluster, published.Namespace, published.target())
	if err != nil {
		return err
	}
	if !b.Exists || b.Image != pCtx.ImageName || (published.Exists && b.UID != published.UID) || (published.FinalGeneration > 0 && b.Generation != published.FinalGeneration) {
		return fmt.Errorf("保存完整站点前线上发布身份已变化")
	}
	store, err := newWebSiteStore(pCtx.Workspace.BaseDir, identity)
	if err != nil {
		return err
	}
	source := filepath.Join(pCtx.Workspace.PipelineDir(), "docker-build", "html")
	if err := store.save(source, identity, published.FinalSHA256, func() error { return webSiteRecheck(ctx, cs, identity) }); err != nil {
		return err
	}
	webLogger(pCtx).printf("正式发布已验证，本地完整站点已更新；Deployment=%s/%s；目录=%s", identity.Namespace, identity.Target, store.root)
	return nil
}
