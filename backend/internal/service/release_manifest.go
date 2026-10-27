package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"df-build-server/internal/model"
)

const ReleaseManifestFileName = "version.json"
const MaxReleaseManifestBytes = 1 << 20

// The manifest is business metadata. Its fileList and other fields are retained
// verbatim and never interpreted as deployment instructions or configuration.
func ParseReleaseManifest(data []byte) (result model.ReleaseMetadata) {
	if len(data) > MaxReleaseManifestBytes {
		result.ReleaseManifestError = "version.json 超过 1 MiB，请精简业务版本说明"
		return
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		result.ReleaseManifestError = "version.json 必须使用 UTF-8 编码"
		return
	}
	if bytes.IndexByte(data, 0) >= 0 {
		result.ReleaseManifestError = "version.json 包含无效空字符"
		return
	}
	result.ReleaseManifestJSON = string(data)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		result.ReleaseManifestError = "version.json 必须是有效的 JSON 对象"
		return
	}
	readText := func(key string, required bool, max int) (string, error) {
		raw, exists := fields[key]
		if (!exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))) && !required {
			return "", nil
		}
		var value string
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return "", fmt.Errorf("version.json 的 %s 必须是文本", key)
		}
		value = strings.TrimSpace(value)
		if strings.ContainsRune(value, 0) {
			return "", fmt.Errorf("version.json 的 %s 包含无效空字符", key)
		}
		if required && value == "" {
			return "", fmt.Errorf("version.json 缺少业务版本号 appVersion")
		}
		if utf8.RuneCountInString(value) > max {
			return "", fmt.Errorf("version.json 的 %s 不能超过 %d 个字符", key, max)
		}
		return value, nil
	}
	var err error
	if result.AppVersion, err = readText("appVersion", true, 128); err == nil {
		if result.ReleaseTime, err = readText("releaseTime", false, 128); err == nil {
			result.ReleaseReadme, err = readText("readme", false, 65536)
		}
	}
	if err != nil {
		result.AppVersion, result.ReleaseTime, result.ReleaseReadme = "", "", ""
		result.ReleaseManifestError = err.Error()
	}
	return
}

// A directory may contain one release manifest, including a nested release
// directory. Multiple manifests are ambiguous; do not choose one arbitrarily.
func ReadReleaseManifest(root string) model.ReleaseMetadata {
	if strings.TrimSpace(root) == "" {
		return model.ReleaseMetadata{}
	}
	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() != ReleaseManifestFileName || entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("version.json 必须是普通文件")
		}
		if found != "" {
			return fmt.Errorf("目录中存在多个 version.json，请按业务版本分别导入")
		}
		found = path
		return nil
	})
	if os.IsNotExist(err) && found == "" {
		return model.ReleaseMetadata{}
	}
	if err != nil {
		return model.ReleaseMetadata{ReleaseManifestError: "读取业务版本失败: " + err.Error()}
	}
	if found == "" {
		return model.ReleaseMetadata{}
	}
	return ReadReleaseManifestFile(found)
}

func ReadReleaseManifestFile(path string) model.ReleaseMetadata {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return model.ReleaseMetadata{ReleaseManifestError: "version.json 必须是可读取的普通文件"}
	}
	f, err := os.Open(path)
	if err != nil {
		return model.ReleaseMetadata{ReleaseManifestError: "读取 version.json 失败: " + err.Error()}
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return model.ReleaseMetadata{ReleaseManifestError: "version.json 必须是普通文件"}
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxReleaseManifestBytes+1))
	if err != nil {
		return model.ReleaseMetadata{ReleaseManifestError: "读取 version.json 失败: " + err.Error()}
	}
	return ParseReleaseManifest(data)
}
