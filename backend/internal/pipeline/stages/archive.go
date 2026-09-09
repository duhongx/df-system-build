package stages

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ZipDirectory creates a zip file containing the specified subdirectory
// e.g. ZipDirectory("/workspace/source", "dist", "/workspace/source/app.zip")
func ZipDirectory(baseDir, subDir, zipFilePath string) error {
	sourceDir := filepath.Join(baseDir, subDir)

	zipFile, err := os.Create(zipFilePath)
	if err != nil {
		return fmt.Errorf("创建 zip 文件失败: %w", err)
	}
	defer zipFile.Close()

	w := zip.NewWriter(zipFile)
	defer w.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from baseDir (to include subDir in zip)
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}

		if info.IsDir() {
			if relPath != "." {
				_, err = w.Create(relPath + "/")
			}
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.Method = zip.Deflate

		writer, err := w.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})
}

// Unzip extracts a zip file to a target directory
func Unzip(zipPath, targetDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开 zip 文件失败: %w", err)
	}
	defer r.Close()

	if len(r.File) > 100000 {
		return fmt.Errorf("ZIP 文件数量超过限制")
	}
	var total uint64
	for _, f := range r.File {
		if f.UncompressedSize64 > 4<<30 || total > (4<<30)-f.UncompressedSize64 {
			return fmt.Errorf("ZIP 解压大小超过 4 GiB")
		}
		total += f.UncompressedSize64
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ZIP 不允许符号链接: %s", f.Name)
		}
		fpath := filepath.Join(targetDir, f.Name)

		// Security: prevent zip slip
		if !strings.HasPrefix(filepath.Clean(fpath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			if filepath.Clean(fpath) != filepath.Clean(targetDir) {
				return fmt.Errorf("非法路径: %s", f.Name)
			}
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		closeErr := outFile.Close()
		rc.Close()
		if closeErr != nil {
			return closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// UntarFromReader extracts a tar stream to a target directory
func UntarFromReader(reader io.Reader, targetDir string) error {
	tr := tar.NewReader(reader)
	var total int64
	files := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 tar 失败: %w", err)
		}

		files++
		if files > 100000 || header.Size < 0 || header.Size > 4<<30 || total > (4<<30)-header.Size {
			return fmt.Errorf("Pod 导出目录超过文件数量或大小限制")
		}
		total += header.Size
		fpath := filepath.Join(targetDir, header.Name)

		// Security: prevent path traversal
		if !strings.HasPrefix(filepath.Clean(fpath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			if filepath.Clean(fpath) != filepath.Clean(targetDir) {
				return fmt.Errorf("tar 包含非法路径: %s", header.Name)
			}
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(outFile, tr)
			closeErr := outFile.Close()
			if copyErr != nil {
				return fmt.Errorf("tar 文件复制失败: %w", copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("Pod 导出不允许链接或特殊文件: %s", header.Name)
		}
	}
	return nil
}
