package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	artifactUploadChunkSize     = int64(8 << 20)
	artifactUploadConcurrency   = 6
	artifactUploadPendingMarker = ".upload-processing"
)

type artifactUploadFile struct {
	Path         string   `json:"path"`
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	LastModified int64    `json:"lastModified"`
	Chunks       []string `json:"chunks"` // SHA256 for durably acknowledged chunks; empty means missing.
}

type artifactUploadFailure struct {
	FileName string `json:"fileName"`
	Error    string `json:"error"`
}

type artifactUploadResult struct {
	BatchID   string                  `json:"batchId"`
	UploadDir string                  `json:"uploadDir"`
	Success   []string                `json:"success"`
	Failed    []artifactUploadFailure `json:"failed"`
	Count     int                     `json:"count"`
}

type artifactUploadSession struct {
	ID             string                `json:"id"`
	OwnerID        uint                  `json:"ownerId"`
	BatchID        string                `json:"batchId"`
	Status         string                `json:"status"`
	ChunkSize      int64                 `json:"chunkSize"`
	MaxConcurrency int                   `json:"maxConcurrency"`
	Files          []artifactUploadFile  `json:"files"`
	ReceivedBytes  int64                 `json:"receivedBytes"`
	TotalBytes     int64                 `json:"totalBytes"`
	CompletedFiles int                   `json:"completedFiles"`
	CurrentFile    string                `json:"currentFile"`
	Error          string                `json:"error"`
	Result         *artifactUploadResult `json:"result,omitempty"`
	UpdatedAt      time.Time             `json:"updatedAt"`
}

type artifactUploadLock struct {
	sync.Mutex
	running bool
}

// Receipts live outside the deployable workspace. No incomplete archive can be
// discovered by source-batch scanning or submitted as a deployment input.
type artifactUploadStore struct {
	root      string
	finalRoot string
	mu        sync.Mutex
	locks     map[string]*artifactUploadLock
	readSlots chan struct{} // bound simultaneous in-memory chunk bodies across sessions
	apps      func() ([]model.Application, error)
	persist   func(artifactVersionInput, []model.ArtifactVersionItem) error
}

func newArtifactUploadStore(root, finalRoot string) *artifactUploadStore {
	return &artifactUploadStore{
		root: root, finalRoot: finalRoot, locks: map[string]*artifactUploadLock{},
		readSlots: make(chan struct{}, artifactUploadConcurrency),
		apps: func() ([]model.Application, error) {
			if repository.DB == nil {
				return nil, fmt.Errorf("数据库未初始化")
			}
			apps, _, err := repository.NewApplicationRepo().List(repository.AppListParams{PageSize: 1000})
			return apps, err
		},
		persist: func(input artifactVersionInput, items []model.ArtifactVersionItem) error {
			if repository.DB == nil {
				return fmt.Errorf("数据库未初始化")
			}
			// Recover an interrupted final acknowledgement without rewriting item IDs
			// in an already committed version (which may now be used by a deployment).
			var existing model.ArtifactVersion
			err := repository.DB.Where("version_no = ?", input.VersionNo).First(&existing).Error
			if err == nil {
				if existing.TargetPath != input.TargetPath {
					return fmt.Errorf("更新记录的目录不一致")
				}
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			_, _, err = persistPreparedArtifactVersion(input, items, countArtifactVersionItems(items))
			return err
		},
	}
}

var defaultArtifactUploads = newArtifactUploadStore(
	filepath.Join(platformBaseDir(), "workspaces", "artifact-upload-sessions"),
	filepath.Join(platformBaseDir(), batchUploadRelativeRoot, "upload"),
)

func (h *BatchDeployHandler) artifactUploads() *artifactUploadStore {
	if h.uploads != nil {
		return h.uploads
	}
	return defaultArtifactUploads
}

func (s *artifactUploadStore) lock(id string) *artifactUploadLock {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[id] == nil {
		s.locks[id] = &artifactUploadLock{}
	}
	return s.locks[id]
}

func validUploadID(id string) bool {
	data, err := hex.DecodeString(id)
	return err == nil && len(data) == 16 && strings.ToLower(id) == id
}

func (s *artifactUploadStore) dir(id string) string { return filepath.Join(s.root, id) }

func (s *artifactUploadStore) load(id string, owner uint) (*artifactUploadSession, error) {
	data, err := os.ReadFile(filepath.Join(s.dir(id), "session.json"))
	if err != nil {
		return nil, err
	}
	var session artifactUploadSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	if session.ID != id || session.OwnerID != owner {
		return nil, os.ErrPermission
	}
	return &session, nil
}

func (s *artifactUploadStore) save(session *artifactUploadSession) error {
	session.UpdatedAt = time.Now()
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	dir := s.dir(session.ID)
	file, err := os.CreateTemp(dir, ".receipt-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "session.json")); err != nil {
		return err
	}
	return syncUploadDir(dir)
}

func syncUploadDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func uploadError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	} else if errors.Is(err, os.ErrPermission) {
		status = http.StatusForbidden
	}
	response.FailWithStatus(c, status, 13001, err.Error())
}

func (h *BatchDeployHandler) CreateArtifactUpload(c *gin.Context) {
	var req struct {
		ID    string               `json:"id"`
		Files []artifactUploadFile `json:"files"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)
	if err := c.ShouldBindJSON(&req); err != nil {
		uploadError(c, fmt.Errorf("上传清单无效: %w", err))
		return
	}
	if !validUploadID(req.ID) || len(req.Files) == 0 || len(req.Files) > 10000 {
		uploadError(c, fmt.Errorf("上传编号或文件数量无效"))
		return
	}
	var total, chunkCount int64
	seen := map[string]bool{}
	for i := range req.Files {
		file := &req.Files[i]
		name, err := safeUploadFileName(file.Path)
		if err != nil || len(file.Path) > 4096 || len(name) > 255 || strings.ContainsRune(file.Path, 0) || file.Size <= 0 || file.Size > 1<<40 {
			uploadError(c, fmt.Errorf("文件名或大小无效: %s", file.Path))
			return
		}
		if name == service.ReleaseManifestFileName && file.Size > service.MaxReleaseManifestBytes {
			uploadError(c, fmt.Errorf("version.json 不能超过 1 MiB"))
			return
		}
		key := strings.ToLower(name)
		if seen[key] {
			uploadError(c, fmt.Errorf("目录内存在同名制品文件: %s", name))
			return
		}
		seen[key] = true
		file.Name = name
		file.Chunks = nil // Never trust client-provided receipts.
		total += file.Size
		chunkCount += (file.Size + artifactUploadChunkSize - 1) / artifactUploadChunkSize
	}
	if chunkCount > 100000 || total > 1<<40 {
		uploadError(c, fmt.Errorf("上传清单过大，请分批选择目录"))
		return
	}
	s := h.artifactUploads()
	lock := s.lock(req.ID)
	lock.Lock()
	defer lock.Unlock()
	owner := middleware.GetCurrentUserID(c)
	session, err := s.load(req.ID, owner)
	if err == nil {
		expected := append([]artifactUploadFile(nil), session.Files...)
		for i := range expected {
			expected[i].Chunks = nil
		}
		if !reflect.DeepEqual(expected, req.Files) || session.Status == "canceled" {
			response.FailWithStatus(c, http.StatusConflict, 13001, "上传清单已改变，请重新上传")
			return
		}
		h.resumeArtifactFinalization(s, lock, session)
		response.OK(c, session)
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		uploadError(c, err)
		return
	}
	if err := os.MkdirAll(filepath.Join(s.dir(req.ID), "files"), 0700); err != nil {
		uploadError(c, fmt.Errorf("创建暂存目录失败: %w", err))
		return
	}
	for i := range req.Files {
		req.Files[i].Chunks = make([]string, (req.Files[i].Size+artifactUploadChunkSize-1)/artifactUploadChunkSize)
	}
	session = &artifactUploadSession{
		ID: req.ID, OwnerID: owner, BatchID: newBatchID(), Status: "uploading",
		ChunkSize: artifactUploadChunkSize, MaxConcurrency: artifactUploadConcurrency,
		Files: req.Files, TotalBytes: total,
	}
	if err := s.save(session); err != nil {
		uploadError(c, fmt.Errorf("保存上传清单失败: %w", err))
		return
	}
	response.OK(c, session)
}

// Called with the session lock held. A fresh process restarts unfinished
// validation when the client reconnects, without asking it to resend bytes.
func (h *BatchDeployHandler) resumeArtifactFinalization(s *artifactUploadStore, lock *artifactUploadLock, session *artifactUploadSession) {
	if session.Status == "processing" && !lock.running {
		lock.running = true
		go s.finalize(session.ID, session.OwnerID, lock)
	}
}

func (h *BatchDeployHandler) GetArtifactUpload(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id := c.Param("uploadID")
	if !validUploadID(id) {
		uploadError(c, fmt.Errorf("上传编号无效"))
		return
	}
	s := h.artifactUploads()
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	session, err := s.load(id, middleware.GetCurrentUserID(c))
	if err != nil {
		uploadError(c, err)
		return
	}
	h.resumeArtifactFinalization(s, lock, session)
	response.OK(c, session)
}

func (h *BatchDeployHandler) PutArtifactUploadChunk(c *gin.Context) {
	id := c.Param("uploadID")
	fileIndex, e1 := strconv.Atoi(c.Param("fileIndex"))
	chunkIndex, e2 := strconv.Atoi(c.Param("chunkIndex"))
	digest := c.GetHeader("X-Chunk-SHA256")
	digestBytes, e3 := hex.DecodeString(digest)
	if !validUploadID(id) || e1 != nil || e2 != nil || e3 != nil || len(digestBytes) != sha256.Size || strings.ToLower(digest) != digest {
		uploadError(c, fmt.Errorf("分片参数无效"))
		return
	}
	s := h.artifactUploads()
	lock := s.lock(id)
	owner := middleware.GetCurrentUserID(c)
	lock.Lock()
	session, err := s.load(id, owner)
	var length int64
	if err == nil {
		if session.Status != "uploading" || fileIndex < 0 || fileIndex >= len(session.Files) || chunkIndex < 0 || chunkIndex >= len(session.Files[fileIndex].Chunks) {
			err = fmt.Errorf("上传状态或分片范围无效")
		} else {
			length = min(session.ChunkSize, session.Files[fileIndex].Size-int64(chunkIndex)*session.ChunkSize)
		}
	}
	lock.Unlock()
	if err != nil {
		uploadError(c, err)
		return
	}
	select {
	case s.readSlots <- struct{}{}:
		defer func() { <-s.readSlots }()
	case <-c.Request.Context().Done():
		return
	}
	// Read only one bounded chunk; a broken request cannot partially overwrite a
	// previously acknowledged region. Network reads run concurrently, outside locks.
	body := make([]byte, length+1)
	n, readErr := io.ReadFull(c.Request.Body, body)
	if int64(n) != length || (readErr != io.EOF && readErr != io.ErrUnexpectedEOF) {
		uploadError(c, fmt.Errorf("分片传输不完整，请重试"))
		return
	}
	body = body[:n]
	actual := sha256.Sum256(body)
	if hex.EncodeToString(actual[:]) != digest {
		uploadError(c, fmt.Errorf("分片校验失败，请重试"))
		return
	}
	lock.Lock()
	defer lock.Unlock()
	session, err = s.load(id, owner)
	if err != nil {
		uploadError(c, err)
		return
	}
	if session.Status != "uploading" {
		response.FailWithStatus(c, http.StatusConflict, 13001, "当前上传已停止接收分片")
		return
	}
	file := &session.Files[fileIndex]
	if existing := file.Chunks[chunkIndex]; existing != "" {
		if existing != digest {
			response.FailWithStatus(c, http.StatusConflict, 13001, "已接收的分片内容不同，请重新上传")
			return
		}
		response.OK(c, gin.H{"receivedBytes": session.ReceivedBytes})
		return
	}
	filePath := filepath.Join(s.dir(id), "files", file.Name)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		_, err = f.WriteAt(body, int64(chunkIndex)*session.ChunkSize)
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		response.FailWithStatus(c, http.StatusInternalServerError, 13001, "写入分片失败: "+err.Error())
		return
	}
	// Data and its directory entry precede the durable receipt, including the
	// first chunk of a new file. A successful reply survives service restarts.
	if err = syncUploadDir(filepath.Dir(filePath)); err == nil {
		file.Chunks[chunkIndex] = digest
		session.ReceivedBytes += length
		err = s.save(session)
	}
	if err != nil {
		response.FailWithStatus(c, http.StatusInternalServerError, 13001, "保存分片进度失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"receivedBytes": session.ReceivedBytes})
}

func (h *BatchDeployHandler) CompleteArtifactUpload(c *gin.Context) {
	id := c.Param("uploadID")
	if !validUploadID(id) {
		uploadError(c, fmt.Errorf("上传编号无效"))
		return
	}
	s := h.artifactUploads()
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	session, err := s.load(id, middleware.GetCurrentUserID(c))
	if err != nil {
		uploadError(c, err)
		return
	}
	if session.Status == "canceled" || session.ReceivedBytes != session.TotalBytes {
		response.FailWithStatus(c, http.StatusConflict, 13001, "文件尚未传输完整")
		return
	}
	for _, file := range session.Files {
		for _, digest := range file.Chunks {
			if digest == "" {
				response.FailWithStatus(c, http.StatusConflict, 13001, "仍有分片未接收")
				return
			}
		}
	}
	if session.Status != "completed" && !lock.running {
		session.Status, session.Error = "processing", ""
		if err := s.save(session); err != nil {
			uploadError(c, err)
			return
		}
		h.resumeArtifactFinalization(s, lock, session)
	}
	response.OK(c, session)
}

func (s *artifactUploadStore) finalize(id string, owner uint, lock *artifactUploadLock) {
	defer func() { lock.Lock(); lock.running = false; lock.Unlock() }()
	lock.Lock()
	session, err := s.load(id, owner)
	lock.Unlock()
	if err != nil {
		return
	}
	progress := func(index int, name string) error {
		lock.Lock()
		defer lock.Unlock()
		current, err := s.load(id, owner)
		if err != nil {
			return err
		}
		current.CompletedFiles, current.CurrentFile = index, name
		return s.save(current)
	}
	result, err := s.publish(session, progress)
	lock.Lock()
	defer lock.Unlock()
	current, loadErr := s.load(id, owner)
	if loadErr != nil {
		return
	}
	if err != nil {
		current.Status, current.Error = "failed", err.Error()
	} else {
		current.Status, current.Result = "completed", result
		current.CompletedFiles, current.CurrentFile = len(current.Files), ""
	}
	if err := s.save(current); err == nil && current.Status == "completed" {
		_ = os.Remove(filepath.Join(s.finalRoot, current.BatchID, artifactUploadPendingMarker))
	}
}

func (s *artifactUploadStore) publish(session *artifactUploadSession, progress func(int, string) error) (*artifactUploadResult, error) {
	stage := filepath.Join(s.dir(session.ID), "files")
	target := filepath.Join(s.finalRoot, session.BatchID)
	if _, err := os.Stat(stage); errors.Is(err, os.ErrNotExist) {
		stage = target // recover a crash after rename, before the receipt was saved
	}
	apps, err := s.apps()
	if err != nil {
		return nil, err
	}
	items := make([]model.ArtifactVersionItem, 0, len(session.Files))
	result := &artifactUploadResult{BatchID: session.BatchID, UploadDir: target, Success: []string{}, Failed: []artifactUploadFailure{}}
	for i, file := range session.Files {
		if err := progress(i, file.Name); err != nil {
			return nil, err
		}
		info, err := os.Stat(filepath.Join(stage, file.Name))
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			return nil, fmt.Errorf("暂存文件不完整: %s", file.Name)
		}
		if file.Name == service.ReleaseManifestFileName {
			continue // Business metadata is persisted with the version, not deployed.
		}
		item := buildArtifactVersionItem(session.BatchID, stage, file.Name, apps)
		items = append(items, item)
		if item.ValidateStatus == "valid" {
			result.Success = append(result.Success, file.Name)
		} else {
			result.Failed = append(result.Failed, artifactUploadFailure{FileName: file.Name, Error: item.StatusReason})
		}
	}
	if err := progress(len(session.Files), "生成更新记录"); err != nil {
		return nil, err
	}
	if stage != target {
		if err := os.MkdirAll(s.finalRoot, 0755); err != nil {
			return nil, err
		}
		marker, err := os.OpenFile(filepath.Join(stage, artifactUploadPendingMarker), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return nil, err
		}
		if _, err = marker.WriteString(session.ID); err == nil {
			err = marker.Sync()
		}
		if closeErr := marker.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		if err := syncUploadDir(stage); err != nil {
			return nil, err
		}
		if err := os.Rename(stage, target); err != nil {
			return nil, fmt.Errorf("发布上传目录失败: %w", err)
		}
		if err := syncUploadDir(s.finalRoot); err != nil {
			return nil, err
		}
	}
	if err := s.persist(artifactVersionInput{
		VersionNo: session.BatchID, SourceType: "upload", SourceLabel: "本地上传",
		Status: "available", LocalDir: target, TargetPath: target,
	}, items); err != nil {
		return nil, fmt.Errorf("生成更新记录失败: %w", err)
	}
	result.Count = len(result.Success)
	return result, nil
}

func (h *BatchDeployHandler) CancelArtifactUpload(c *gin.Context) {
	id := c.Param("uploadID")
	if !validUploadID(id) {
		uploadError(c, fmt.Errorf("上传编号无效"))
		return
	}
	s := h.artifactUploads()
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	session, err := s.load(id, middleware.GetCurrentUserID(c))
	if err != nil {
		uploadError(c, err)
		return
	}
	if lock.running || session.Status == "processing" || session.Status == "completed" {
		response.FailWithStatus(c, http.StatusConflict, 13001, "文件正在校验或已生成记录，请从更新记录管理")
		return
	}
	if _, err := os.Stat(filepath.Join(s.finalRoot, session.BatchID)); err == nil {
		response.FailWithStatus(c, http.StatusConflict, 13001, "目录已发布，请重试生成更新记录")
		return
	}
	// Persist the tombstone first: a crash during cleanup must never accept more chunks.
	session.Status = "canceled"
	if err = s.save(session); err == nil {
		err = os.RemoveAll(filepath.Join(s.dir(id), "files"))
	}
	if err != nil {
		uploadError(c, err)
		return
	}
	response.OK(c, gin.H{"id": id})
}
