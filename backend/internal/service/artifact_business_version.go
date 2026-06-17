package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

func ExtractPackageBusinessVersion(artifactPath, appType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(appType)) {
	case "vue":
		return extractVuePackageBusinessVersion(artifactPath)
	case "java":
		return extractJavaPackageBusinessVersion(artifactPath)
	default:
		return "", fmt.Errorf("不支持的应用类型: %s", appType)
	}
}

func BusinessVersionsEqual(appType, expected, actual string) bool {
	expected = strings.TrimSpace(expected)
	actual = strings.TrimSpace(actual)
	if expected == "" || actual == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(appType)) {
	case "java":
		return javaVersionField(expected, "branch") == javaVersionField(actual, "branch") &&
			javaVersionField(expected, "commit") == javaVersionField(actual, "commit") &&
			javaVersionField(expected, "time") == javaVersionField(actual, "time")
	case "vue":
		for _, key := range []string{"xiTongId", "version", "date", "branch", "commit"} {
			if jsonStringField(expected, key) != jsonStringField(actual, key) {
				return false
			}
		}
		return true
	default:
		return compactJSON(expected) == compactJSON(actual)
	}
}

func extractVuePackageBusinessVersion(artifactPath string) (string, error) {
	return readFirstZipEntryAsCompactJSON(artifactPath, func(name string) bool {
		clean := filepath.ToSlash(name)
		return clean == "config.json" || clean == "dist/config.json"
	})
}

func extractJavaPackageBusinessVersion(artifactPath string) (string, error) {
	props, err := readFirstZipEntry(artifactPath, func(name string) bool {
		return filepath.ToSlash(name) == "BOOT-INF/classes/git.properties"
	})
	if err != nil {
		return "", err
	}
	values := parseProperties(string(props))
	payload := map[string]any{
		"git": map[string]any{
			"branch": values["git.branch"],
			"commit": map[string]any{
				"id":   firstNonEmpty(values["git.commit.id.abbrev"], values["git.commit.id"]),
				"time": values["git.commit.time"],
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readFirstZipEntryAsCompactJSON(artifactPath string, match func(string) bool) (string, error) {
	data, err := readFirstZipEntry(artifactPath, match)
	if err != nil {
		return "", err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("解析 config.json 失败: %w", err)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readFirstZipEntry(artifactPath string, match func(string) bool) ([]byte, error) {
	zr, err := zip.OpenReader(artifactPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !match(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("未找到业务版本文件")
}

func parseProperties(content string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func javaVersionField(raw, field string) string {
	var payload struct {
		Git struct {
			Branch string `json:"branch"`
			Commit struct {
				ID       string `json:"id"`
				IDAbbrev string `json:"idAbbrev"`
				Time     string `json:"time"`
			} `json:"commit"`
		} `json:"git"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	switch field {
	case "branch":
		return payload.Git.Branch
	case "commit":
		return firstNonEmpty(payload.Git.Commit.IDAbbrev, payload.Git.Commit.ID)
	case "time":
		return payload.Git.Commit.Time
	default:
		return ""
	}
}

func jsonStringField(raw, key string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

func compactJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		return strings.TrimSpace(raw)
	}
	return buf.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
