package service

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/model"
	"gorm.io/gorm"
)

type LogCollectionService struct {
	db      *gorm.DB
	root    string
	connect func(context.Context, model.Server) (logRemote, error)
	mu      sync.Mutex
	running map[uint]context.CancelFunc
}

func NewLogCollectionService(db *gorm.DB, workspace string) *LogCollectionService {
	return &LogCollectionService{db: db, root: filepath.Join(workspace, "log-collections"), connect: connectLogRemote, running: make(map[uint]context.CancelFunc)}
}

func validateLogSource(source *model.LogSource) error {
	source.Name = strings.TrimSpace(source.Name)
	source.Directory = strings.TrimSpace(source.Directory)
	source.Pattern = strings.TrimSpace(source.Pattern)
	if source.Name == "" || len(source.Name) > 128 || source.ServerID == 0 {
		return errors.New("请填写来源名称并选择主机（名称最长 128 字节）")
	}
	switch source.Category {
	case "microservice", "database", "rabbitmq", "redis", "other":
	default:
		return errors.New("请选择有效的日志类型")
	}
	if !path.IsAbs(source.Directory) || strings.ContainsRune(source.Directory, 0) {
		return errors.New("日志目录必须是主机上的绝对路径")
	}
	source.Directory = path.Clean(source.Directory)
	if source.Pattern == "" {
		source.Pattern = "*.log,*.log.*,*.gz"
	}
	for _, pattern := range logPatterns(source.Pattern) {
		if strings.Contains(pattern, "/") {
			return errors.New("文件规则只匹配文件名，不能包含目录；多个规则用逗号或换行分隔")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("文件规则 %q 无效: %w", pattern, err)
		}
	}
	if len(logPatterns(source.Pattern)) == 0 {
		return errors.New("请至少填写一个文件匹配规则")
	}
	return nil
}

func logPatterns(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	var result []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			result = append(result, p)
		}
	}
	return result
}

func logMatches(source model.LogSource, relative string) bool {
	if !source.Recursive && strings.Contains(relative, "/") {
		return false
	}
	for _, pattern := range logPatterns(source.Pattern) {
		if matched, _ := path.Match(pattern, path.Base(relative)); matched {
			return true
		}
	}
	return false
}

func validLogRelative(p string) bool {
	return p != "" && p != "." && !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00")
}

func (s *LogCollectionService) Sources() ([]model.LogSource, error) {
	result := []model.LogSource{}
	err := s.db.Order("category, name, id").Find(&result).Error
	return result, err
}

func (s *LogCollectionService) SaveSource(source model.LogSource) (*model.LogSource, error) {
	if err := validateLogSource(&source); err != nil {
		return nil, err
	}
	var host model.Server
	if err := s.db.First(&host, source.ServerID).Error; err != nil {
		return nil, fmt.Errorf("主机不存在或读取失败: %w", err)
	}
	if source.ID != 0 {
		var old model.LogSource
		if err := s.db.First(&old, source.ID).Error; err != nil {
			return nil, err
		}
		source.CreatedAt = old.CreatedAt
	}
	err := s.db.Save(&source).Error
	return &source, err
}

func (s *LogCollectionService) DeleteSource(id uint) error {
	return s.db.Delete(&model.LogSource{}, id).Error
}

type LogSourceFile struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

func listLogFiles(ctx context.Context, remote logRemote, source model.LogSource) ([]LogSourceFile, error) {
	root, err := remote.RealPath(source.Directory)
	if err != nil {
		return nil, fmt.Errorf("读取日志目录 %s 失败: %w", source.Directory, err)
	}
	files := []LogSourceFile{}
	visited := 0
	var walk func(string, int) error
	walk = func(relative string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 32 {
			return errors.New("目录层级超过 32，请缩小日志目录范围")
		}
		entries, err := remote.ReadDir(path.Join(root, relative))
		if err != nil {
			return fmt.Errorf("读取目录 %s 失败: %w", path.Join(source.Directory, relative), err)
		}
		for _, entry := range entries {
			visited++
			if visited > 20000 {
				return errors.New("目录条目超过 20000，请缩小目录范围")
			}
			name := path.Join(relative, entry.Name())
			if !validLogRelative(name) || entry.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() && source.Recursive {
				if err := walk(name, depth+1); err != nil {
					return err
				}
			}
			if entry.Mode().IsRegular() && logMatches(source, name) {
				files = append(files, LogSourceFile{Path: name, Size: entry.Size(), ModifiedAt: entry.ModTime()})
				if len(files) > 5000 {
					return errors.New("匹配文件超过 5000，请缩小目录或文件规则")
				}
			}
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModifiedAt.After(files[j].ModifiedAt) })
	return files, nil
}

func (s *LogCollectionService) Files(ctx context.Context, sourceID uint) ([]LogSourceFile, error) {
	var source model.LogSource
	if err := s.db.First(&source, sourceID).Error; err != nil {
		return nil, err
	}
	var host model.Server
	if err := s.db.First(&host, source.ServerID).Error; err != nil {
		return nil, err
	}
	remote, err := s.connect(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("主机 %s: %w", host.Host, err)
	}
	defer remote.Close()
	return listLogFiles(ctx, remote, source)
}

type LogCollectionSelection struct {
	SourceID uint     `json:"sourceId"`
	Files    []string `json:"files"`
}

func (s *LogCollectionService) Start(selections []LogCollectionSelection, operator string) (*model.LogCollectionJob, error) {
	job := model.LogCollectionJob{Status: "running", Operator: operator, Message: "准备连接主机", Items: []model.LogCollectionItem{}}
	hosts := map[uint]model.Server{}
	seen := map[string]bool{}
	for _, selection := range selections {
		if selection.SourceID == 0 {
			return nil, errors.New("日志来源无效")
		}
		var source model.LogSource
		if err := s.db.First(&source, selection.SourceID).Error; err != nil {
			return nil, fmt.Errorf("读取日志来源失败: %w", err)
		}
		var host model.Server
		if err := s.db.First(&host, source.ServerID).Error; err != nil {
			return nil, fmt.Errorf("来源 %s 的主机不存在或读取失败: %w", source.Name, err)
		}
		hosts[host.ID] = host
		for _, name := range selection.Files {
			if !validLogRelative(name) || !logMatches(source, name) {
				return nil, fmt.Errorf("来源 %s 的文件路径或匹配规则不符: %s", source.Name, name)
			}
			key := fmt.Sprintf("%d/%s", source.ID, name)
			if seen[key] {
				continue
			}
			seen[key] = true
			item := model.LogCollectionItem{Source: source, Host: host.Host, Path: name, Status: "pending"}
			item.ArchivePath = path.Join(source.Category, fmt.Sprintf("%s-%d", logArchiveSegment(host.Host), host.ID), fmt.Sprintf("%s-%d", logArchiveSegment(source.Name), source.ID), name)
			job.Items = append(job.Items, item)
		}
	}
	if len(job.Items) == 0 || len(job.Items) > 5000 {
		return nil, errors.New("每次请选择 1 至 5000 个日志文件")
	}
	job.TotalFiles = len(job.Items)
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return nil, fmt.Errorf("创建日志收集目录失败: %w", err)
	}
	if err := s.db.Create(&job).Error; err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.running[job.ID] = cancel
	s.mu.Unlock()
	// The response and worker must not share the mutable Items slice.
	result := job
	result.Items = append([]model.LogCollectionItem(nil), job.Items...)
	go func() {
		defer func() { cancel(); s.mu.Lock(); delete(s.running, job.ID); s.mu.Unlock() }()
		publish := func() error { return s.db.Save(&job).Error }
		err := collectLogArchive(ctx, &job, s.jobDir(job.ID), func(source model.LogSource) (logRemote, error) { return s.connect(ctx, hosts[source.ServerID]) }, publish)
		if err != nil {
			job.Status = "failed"
			job.Message = err.Error()
			job.ArchiveSize = 0
			for i := range job.Items {
				if job.Items[i].Status == "running" || job.Items[i].Status == "pending" {
					job.Items[i].Status = "failed"
					job.Items[i].Error = "任务中止: " + err.Error()
				}
			}
			_ = os.RemoveAll(s.jobDir(job.ID))
			now := time.Now()
			job.FinishedAt = &now
		}
		countLogResults(&job)
		if err := publish(); err != nil {
			fmt.Fprintf(os.Stderr, "日志收集任务 %d 保存最终状态失败: %v\n", job.ID, err)
		}
	}()
	return &result, nil
}

func logArchiveSegment(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r < 32 {
			return '_'
		}
		return r
	}, value)
}

func countLogResults(job *model.LogCollectionJob) {
	job.TotalFiles = len(job.Items)
	job.SuccessFiles = 0
	job.FailedFiles = 0
	for _, item := range job.Items {
		if item.Status == "success" {
			job.SuccessFiles++
		}
		if item.Status == "failed" {
			job.FailedFiles++
		}
	}
}

func (s *LogCollectionService) jobDir(id uint) string {
	return filepath.Join(s.root, strconv.FormatUint(uint64(id), 10))
}
func (s *LogCollectionService) ArchivePath(id uint) string {
	return filepath.Join(s.jobDir(id), "logs.zip")
}
func (s *LogCollectionService) Get(id uint) (*model.LogCollectionJob, error) {
	var job model.LogCollectionJob
	err := s.db.First(&job, id).Error
	return &job, err
}
func (s *LogCollectionService) Jobs(page int) ([]model.LogCollectionJob, int64, error) {
	jobs := []model.LogCollectionJob{}
	var total int64
	if err := s.db.Model(&model.LogCollectionJob{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := s.db.Omit("items").Order("id DESC").Offset((page - 1) * 20).Limit(20).Find(&jobs).Error
	return jobs, total, err
}
func (s *LogCollectionService) Cancel(id uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel := s.running[id]; cancel != nil {
		cancel()
		return nil
	}
	return errors.New("任务已经结束，请刷新状态")
}
func (s *LogCollectionService) Delete(id uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] != nil {
		return errors.New("请先停止收集，等待任务结束后再删除")
	}
	if err := os.RemoveAll(s.jobDir(id)); err != nil {
		return fmt.Errorf("删除收集包失败: %w", err)
	}
	return s.db.Delete(&model.LogCollectionJob{}, id).Error
}

func (s *LogCollectionService) RecoverInterrupted() error {
	var jobs []model.LogCollectionJob
	if err := s.db.Where("status = ?", "running").Find(&jobs).Error; err != nil {
		return err
	}
	for _, job := range jobs {
		job.Status = "failed"
		job.Message = "服务重启，日志收集中断，请重新收集"
		job.ArchiveSize = 0
		for i := range job.Items {
			if job.Items[i].Status != "success" {
				job.Items[i].Status = "failed"
				job.Items[i].Error = job.Message
			}
		}
		now := time.Now()
		job.FinishedAt = &now
		countLogResults(&job)
		if err := os.RemoveAll(s.jobDir(job.ID)); err != nil {
			return err
		}
		if err := s.db.Save(&job).Error; err != nil {
			return err
		}
	}
	return nil
}

type logStorageError struct{ error }

// Stage one complete file before adding a ZIP entry. A remote read failure must
// not leave a truncated file that looks successful in the downloaded archive.
func stageLogFile(ctx context.Context, remote logRemote, item *model.LogCollectionItem, temp string, progress func() error) (err error) {
	root, err := remote.RealPath(item.Source.Directory)
	if err != nil {
		return fmt.Errorf("解析日志目录失败: %w", err)
	}
	resolved, err := remote.RealPath(path.Join(root, item.Path))
	if err != nil {
		return fmt.Errorf("定位文件失败: %w", err)
	}
	if !strings.HasPrefix(resolved, strings.TrimRight(root, "/")+"/") {
		return errors.New("文件实际路径超出配置的日志目录")
	}
	file, err := remote.OpenFile(resolved)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("读取文件信息失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return errors.New("只支持普通日志文件")
	}
	item.Size = info.Size()
	item.ModifiedAt = info.ModTime()
	local, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return logStorageError{fmt.Errorf("创建本地临时文件失败: %w", err)}
	}
	defer func() {
		closeErr := local.Close()
		if err == nil && closeErr != nil {
			err = logStorageError{fmt.Errorf("关闭本地临时文件失败: %w", closeErr)}
		}
	}()
	if err := progress(); err != nil {
		return err
	}
	buffer := make([]byte, 128*1024)
	lastUpdate := time.Now()
	for item.Bytes < item.Size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := min(int64(len(buffer)), item.Size-item.Bytes)
		n, readErr := io.ReadFull(file, buffer[:want])
		if n > 0 {
			written, writeErr := local.Write(buffer[:n])
			item.Bytes += int64(written)
			if writeErr != nil {
				return logStorageError{fmt.Errorf("保存收集文件失败: %w", writeErr)}
			}
		}
		if readErr != nil {
			return fmt.Errorf("读取日志不完整（已读取 %d/%d 字节，文件可能已截断或连接中断）: %w", item.Bytes, item.Size, readErr)
		}
		if time.Since(lastUpdate) >= time.Second {
			if err := progress(); err != nil {
				return err
			}
			lastUpdate = time.Now()
		}
	}
	return ctx.Err()
}

func collectLogArchive(ctx context.Context, job *model.LogCollectionJob, dir string, connect func(model.LogSource) (logRemote, error), publish func() error) (err error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建收集目录失败: %w", err)
	}
	temp := filepath.Join(dir, "current.tmp")
	defer os.Remove(temp)
	output, err := os.OpenFile(filepath.Join(dir, "logs.zip.part"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	archive := zip.NewWriter(output)
	remotes := map[uint]logRemote{}
	connectionErrors := map[uint]error{}
	defer func() {
		for _, remote := range remotes {
			_ = remote.Close()
		}
	}()
	persist := func() error {
		if err := publish(); err != nil {
			return fmt.Errorf("保存收集进度失败: %w", err)
		}
		return nil
	}
	for i := range job.Items {
		item := &job.Items[i]
		if ctx.Err() != nil {
			item.Status = "canceled"
			item.Error = "用户停止收集"
			continue
		}
		item.Status = "running"
		job.Message = fmt.Sprintf("[%d/%d] 主机 %s，来源 %s，文件 %s", i+1, len(job.Items), item.Host, item.Source.Name, path.Join(item.Source.Directory, item.Path))
		if err := persist(); err != nil {
			return err
		}
		remote, ok := remotes[item.Source.ServerID]
		readErr := connectionErrors[item.Source.ServerID]
		if !ok && readErr == nil {
			remote, readErr = connect(item.Source)
			if readErr == nil {
				remotes[item.Source.ServerID] = remote
			} else {
				connectionErrors[item.Source.ServerID] = readErr
			}
		}
		var saveErr error
		if readErr == nil {
			readErr = stageLogFile(ctx, remote, item, temp, func() error { saveErr = persist(); return saveErr })
		}
		if saveErr != nil {
			return saveErr
		}
		var storageErr logStorageError
		if errors.As(readErr, &storageErr) {
			return storageErr
		}
		if readErr != nil {
			item.Status = "failed"
			item.Error = fmt.Sprintf("主机 %s，文件 %s: %v", item.Host, path.Join(item.Source.Directory, item.Path), readErr)
			if ctx.Err() != nil {
				item.Status = "canceled"
				item.Error = "用户停止收集"
			} else {
				job.FailedFiles++
			}
		} else {
			job.Message = "正在压缩: " + item.ArchivePath
			if err := persist(); err != nil {
				return err
			}
			local, err := os.Open(temp)
			if err != nil {
				return err
			}
			header := &zip.FileHeader{Name: item.ArchivePath, Method: zip.Deflate, Modified: item.ModifiedAt}
			switch strings.ToLower(path.Ext(item.Path)) {
			case ".gz", ".zip", ".xz", ".bz2", ".zst":
				header.Method = zip.Store
			}
			header.SetMode(0600)
			entry, zipErr := archive.CreateHeader(header)
			if zipErr == nil {
				_, zipErr = io.Copy(entry, local)
			}
			_ = local.Close()
			if zipErr != nil {
				return fmt.Errorf("制作 ZIP 失败: %w", zipErr)
			}
			item.Status = "success"
			job.SuccessFiles++
		}
		_ = os.Remove(temp)
		if err := persist(); err != nil {
			return err
		}
	}
	job.Status = "completed"
	if job.FailedFiles > 0 {
		job.Status = "partial"
	}
	if job.SuccessFiles == 0 {
		job.Status = "failed"
	}
	if ctx.Err() != nil {
		job.Status = "canceled"
	}
	job.Message = fmt.Sprintf("收集结束：成功 %d，失败 %d，未完成 %d", job.SuccessFiles, job.FailedFiles, len(job.Items)-job.SuccessFiles-job.FailedFiles)
	now := time.Now()
	job.FinishedAt = &now
	manifest := struct {
		Note string                  `json:"note"`
		Job  *model.LogCollectionJob `json:"job"`
	}{"仅包含成功收集的文件；每个文件按开始读取时的大小截取，之后追加的日志不在本包中；文件修改时间筛选不代表日志内容时间范围。", job}
	entry, err := archive.Create("collection-manifest.json")
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(entry)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("完成 ZIP 失败: %w", err)
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, "logs.zip.part"), filepath.Join(dir, "logs.zip")); err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(dir, "logs.zip"))
	if err != nil {
		return err
	}
	job.ArchiveSize = info.Size()
	return nil
}
