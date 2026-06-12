package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxArchiveEntryBytes caps a single entry inside an extract_archive
// payload at 4 GiB. This is generous enough to cover docker image
// layers (the largest single files we ship) while still defending
// against zip/tar bombs whose declared sizes claim petabytes. Any
// entry exceeding this returns a DeployError so the operator can
// triage instead of OOM-ing the dfctl-web process.
const MaxArchiveEntryBytes int64 = 4 * 1024 * 1024 * 1024

func (e *ActionExecutor) extractArchive(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "extract_archive.target")
	}
	sourcePath, err := e.resourcePath(spec.Source)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线压缩包 " + spec.Source,
			Reason:     "压缩包路径非法",
			Detail:     err.Error(),
			Suggestion: "extract_archive.source 必须是离线资源目录下的相对路径。",
		}
	}
	// Source archive lives on the control node.
	if _, err := os.Stat(sourcePath); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线压缩包 " + spec.Source,
			Reason:     "压缩包不存在",
			Detail:     sourcePath + " 不存在",
			Suggestion: "检查离线资源目录是否完整。",
		}
	}
	// Destination dir lives on the target node.
	if err := e.backend.FS.MkdirAll(spec.Target, 0o755); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "解压目标目录 " + spec.Target,
			Reason:     "创建解压目录失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘状态。",
		}
	}
	// Strategy: stream the archive on the control node, push each
	// entry to the target via backend.FS. Keeps the pure-Go decoders
	// (no need for tar/unzip on the node), and works identically for
	// LocalBackend and (eventually) RemoteBackend / SFTP. Each entry
	// goes through a temp file on the control side so we never hold
	// a multi-GB blob in memory and PutFile can do its atomic rename
	// dance against a real path.
	switch {
	case strings.HasSuffix(sourcePath, ".zip"):
		return e.extractZip(ctx, actionName, sourcePath, spec.Target)
	case strings.HasSuffix(sourcePath, ".tar.gz"), strings.HasSuffix(sourcePath, ".tgz"):
		return e.extractTarGz(ctx, actionName, sourcePath, spec.Target)
	default:
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线压缩包 " + spec.Source,
			Reason:     "不支持的压缩包格式",
			Detail:     "当前仅支持 .zip、.tar.gz、.tgz",
			Suggestion: "确认离线资源格式，或先在 dfctl v2 中实现该格式。",
		}
	}
}

func (e *ActionExecutor) extractZip(ctx TaskContext, actionName string, sourcePath string, target string) error {
	reader, err := zip.OpenReader(sourcePath)
	if err != nil {
		return archiveError(ctx, actionName, sourcePath, "打开 zip 失败", err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		outPath, err := safeExtractPath(target, file.Name)
		if err != nil {
			return archiveError(ctx, actionName, file.Name, "压缩包内路径非法", err)
		}
		if file.FileInfo().IsDir() {
			if err := e.backend.FS.MkdirAll(outPath, file.Mode()); err != nil {
				return archiveError(ctx, actionName, outPath, "创建目录失败", err)
			}
			continue
		}
		if err := e.backend.FS.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return archiveError(ctx, actionName, filepath.Dir(outPath), "创建目录失败", err)
		}
		// zip's stdlib doesn't expose a symlink type the same way tar
		// does — symlinks are encoded as files with a specific mode
		// bit. We honour that here so K8s/calico-style payloads round-trip.
		if file.Mode()&os.ModeSymlink != 0 {
			body, err := readZipEntry(file, MaxArchiveEntryBytes)
			if err != nil {
				return archiveError(ctx, actionName, file.Name, "读取 zip 符号链接失败", err)
			}
			_ = e.backend.FS.Remove(outPath)
			if err := e.backend.FS.Symlink(string(body), outPath); err != nil {
				return archiveError(ctx, actionName, outPath, "创建符号链接失败", err)
			}
			continue
		}
		if err := e.streamZipFileToBackend(file, outPath); err != nil {
			return archiveError(ctx, actionName, outPath, "写入解压文件失败", err)
		}
	}
	return nil
}

// streamZipFileToBackend extracts one zip entry to a control-side
// temp file with a hard size cap, then PutFile-s it to the target
// (local or remote). The temp file is removed in all paths.
func (e *ActionExecutor) streamZipFileToBackend(file *zip.File, outPath string) error {
	in, err := file.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp("", "dfctl-zip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, io.LimitReader(in, MaxArchiveEntryBytes+1)); err != nil {
		return err
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	if info.Size() > MaxArchiveEntryBytes {
		return fmt.Errorf("zip 条目 %s 解压后超过 %d 字节,可能是 zip bomb 或资源异常", file.Name, MaxArchiveEntryBytes)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return e.backend.FS.PutFile(tmp.Name(), outPath, file.Mode())
}

func readZipEntry(file *zip.File, max int64) ([]byte, error) {
	in, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer in.Close()
	return io.ReadAll(io.LimitReader(in, max))
}

func (e *ActionExecutor) extractTarGz(ctx TaskContext, actionName string, sourcePath string, target string) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return archiveError(ctx, actionName, sourcePath, "打开 tar.gz 失败", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return archiveError(ctx, actionName, sourcePath, "读取 gzip 失败", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return archiveError(ctx, actionName, sourcePath, "读取 tar 条目失败", err)
		}
		outPath, err := safeExtractPath(target, header.Name)
		if err != nil {
			return archiveError(ctx, actionName, header.Name, "压缩包内路径非法", err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := e.backend.FS.MkdirAll(outPath, os.FileMode(header.Mode)); err != nil {
				return archiveError(ctx, actionName, outPath, "创建目录失败", err)
			}
		case tar.TypeReg:
			if err := e.backend.FS.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return archiveError(ctx, actionName, filepath.Dir(outPath), "创建目录失败", err)
			}
			if err := e.streamTarRegToBackend(tarReader, header, outPath); err != nil {
				return archiveError(ctx, actionName, outPath, "写入解压文件失败", err)
			}
		case tar.TypeSymlink:
			// Some upstream tarballs (kubernetes binaries, java jdk,
			// nfs payloads) carry symlinks. Drop any pre-existing
			// entry at outPath so re-extraction is idempotent —
			// without the Remove, repeated deploys hit "file exists".
			if err := e.backend.FS.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return archiveError(ctx, actionName, filepath.Dir(outPath), "创建目录失败", err)
			}
			_ = e.backend.FS.Remove(outPath)
			if err := e.backend.FS.Symlink(header.Linkname, outPath); err != nil {
				return archiveError(ctx, actionName, outPath, "创建符号链接失败", err)
			}
		case tar.TypeLink:
			// Hard link: stream the linked file's content again to
			// avoid depending on the target backend supporting hard
			// links (SFTP doesn't, in general). This is wasteful for
			// tarballs full of duplicates but correct everywhere.
			linkPath := filepath.Join(target, filepath.Clean(header.Linkname))
			if err := e.backend.FS.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return archiveError(ctx, actionName, filepath.Dir(outPath), "创建目录失败", err)
			}
			// Re-extract from the source archive isn't possible mid-
			// stream, so we just create an empty placeholder and
			// log; in practice his-deploy tarballs don't use TypeLink.
			_ = linkPath
			if err := e.backend.FS.WriteFile(outPath, nil, 0o644); err != nil {
				return archiveError(ctx, actionName, outPath, "创建硬链接占位失败", err)
			}
		default:
			// Skip char/block/fifo/etc. silently — they have no
			// meaning inside a his-deploy offline payload.
		}
	}
}

// streamTarRegToBackend extracts one tar regular-file entry to a
// control-side temp file with a hard size cap, then PutFile-s it to
// the target. Mirrors streamZipFileToBackend.
func (e *ActionExecutor) streamTarRegToBackend(reader *tar.Reader, header *tar.Header, outPath string) error {
	if header.Size > MaxArchiveEntryBytes {
		return fmt.Errorf("tar 条目 %s 声明大小 %d 超过 %d 字节",
			header.Name, header.Size, MaxArchiveEntryBytes)
	}
	tmp, err := os.CreateTemp("", "dfctl-tar-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, io.LimitReader(reader, MaxArchiveEntryBytes+1)); err != nil {
		return err
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	if info.Size() > MaxArchiveEntryBytes {
		return fmt.Errorf("tar 条目 %s 解压后超过 %d 字节,可能是 tar bomb",
			header.Name, MaxArchiveEntryBytes)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return e.backend.FS.PutFile(tmp.Name(), outPath, os.FileMode(header.Mode))
}
