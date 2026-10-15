package frontend

import (
	"archive/zip"
	"fmt"
	"io"
	"path"
	"strings"
)

const MaxExpandedBytes uint64 = 4 << 30

// ValidateArchive reads every entry (including CRC), before extraction or claims.
func ValidateArchive(file string) error {
	r, err := zip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("无法读取压缩包: %w", err)
	}
	defer r.Close()
	if len(r.File) == 0 || len(r.File) > 100000 {
		return fmt.Errorf("压缩包为空或文件数量超过限制")
	}
	seen := map[string]bool{}
	var total uint64
	for _, f := range r.File {
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" || name == "." {
			if f.FileInfo().IsDir() {
				continue
			}
			return fmt.Errorf("压缩包包含空路径")
		}
		if strings.ContainsAny(name, "\\\x00\r\n") || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.Contains(name, ":") || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("压缩包包含非法路径: %s", f.Name)
		}
		if seen[name] {
			return fmt.Errorf("压缩包包含重复路径: %s", name)
		}
		seen[name] = true
		if !f.Mode().IsRegular() && !f.FileInfo().IsDir() {
			return fmt.Errorf("压缩包不允许链接或特殊文件: %s", name)
		}
		if f.UncompressedSize64 > MaxExpandedBytes || total > MaxExpandedBytes-f.UncompressedSize64 {
			return fmt.Errorf("压缩包解压大小超过 4 GiB")
		}
		total += f.UncompressedSize64
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		n, readErr := io.Copy(io.Discard, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		closeErr := rc.Close()
		if readErr != nil || closeErr != nil || uint64(n) != f.UncompressedSize64 {
			return fmt.Errorf("压缩包文件校验失败: %s (%v, %v)", name, readErr, closeErr)
		}
	}
	return nil
}

// ExpandedSize is used by callers for preflight disk capacity checks.
func ExpandedSize(file string) (uint64, error) {
	r, err := zip.OpenReader(file)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	var n uint64
	for _, f := range r.File {
		n += f.UncompressedSize64
	}
	return n, nil
}
