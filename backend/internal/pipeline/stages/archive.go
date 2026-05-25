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

	for _, f := range r.File {
		fpath := filepath.Join(targetDir, f.Name)

		// Security: prevent zip slip
		if !strings.HasPrefix(filepath.Clean(fpath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			if filepath.Clean(fpath) != filepath.Clean(targetDir) {
				return fmt.Errorf("非法路径: %s", f.Name)
			}
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, 0755)
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
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// UntarFromReader extracts a tar stream to a target directory
func UntarFromReader(reader io.Reader, targetDir string) error {
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 tar 失败: %w", err)
		}

		fpath := filepath.Join(targetDir, header.Name)

		// Security: prevent path traversal
		if !strings.HasPrefix(filepath.Clean(fpath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			if filepath.Clean(fpath) != filepath.Clean(targetDir) {
				continue
			}
		}

		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(fpath, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(fpath), 0755)
			outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			io.Copy(outFile, tr)
			outFile.Close()
		}
	}
	return nil
}
