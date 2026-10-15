package frontend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

type File struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Files map[string]File

type Region struct {
	AppName  string `json:"appName"`
	Role     string `json:"role"`
	AppCode  string `json:"appCode,omitempty"`
	Path     string `json:"path"`
	Before   Files  `json:"before,omitempty"`
	Current  Files  `json:"current"`
	Retained Files  `json:"retained,omitempty"`
}

func TreeHash(root string) (string, error) {
	files, err := Inventory(root)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for file := range files {
		paths = append(paths, file)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, file := range paths {
		fmt.Fprintf(h, "%s\x00%s\n", file, files[file].SHA256)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Inventory(root string) (Files, error) {
	files := Files{}
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("站点不允许链接或特殊文件: %s", file)
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, e := io.Copy(h, f)
		c := f.Close()
		if e != nil {
			return e
		}
		if c != nil {
			return c
		}
		files[filepath.ToSlash(rel)] = File{SHA256: hex.EncodeToString(h.Sum(nil)), Size: info.Size()}
		return nil
	})
	return files, err
}

func IsImmutable(file string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".js", ".mjs", ".css", ".map", ".wasm", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".svg", ".woff", ".woff2", ".ttf", ".eot", ".pdf":
		return true
	}
	return false
}

func CheckIdentity(root, expected string) error {
	data, err := os.ReadFile(filepath.Join(root, "config.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("config.json 格式无效: %w", err)
	}
	if raw, ok := cfg["xiTongId"]; ok && expected != "" {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil || id != expected {
			return fmt.Errorf("制品身份与登记编号 %s 不一致（xiTongId 必须为相同字符串）", expected)
		}
	}
	return nil
}

// Reference paths are parsed as HTML rather than by quote-sensitive regexes.
func References(root, prefix string) ([]string, error) {
	f, err := os.Open(filepath.Join(root, "index.html"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := html.Parse(f)
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	scripts := 0
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[a.Key] = a.Val
			}
			ref := ""
			if n.Data == "script" && attrs["src"] != "" {
				ref = attrs["src"]
				scripts++
			}
			if n.Data == "link" && (attrs["rel"] == "stylesheet" || attrs["rel"] == "preload" || attrs["rel"] == "modulepreload" || attrs["rel"] == "prefetch") {
				ref = attrs["href"]
			}
			if ref != "" {
				u, err := url.Parse(ref)
				if err != nil {
					return fmt.Errorf("无效资源引用: %s", ref)
				}
				if u.IsAbs() || u.Host != "" || strings.HasPrefix(ref, "//") {
					return nil
				}
				decoded, err := url.PathUnescape(u.EscapedPath())
				if err != nil {
					return err
				}
				if strings.ContainsAny(decoded, "\\\x00\r\n") {
					return fmt.Errorf("非法资源路径")
				}
				if !strings.HasPrefix(decoded, "/") {
					decoded = path.Join(prefix, decoded)
				}
				clean := path.Clean(decoded)
				if !strings.HasPrefix(clean, prefix) && !strings.HasPrefix(clean, "/apps/cdn/") {
					return fmt.Errorf("资源越出应用目录 %s: %s", prefix, ref)
				}
				refs[strings.TrimPrefix(clean, "/")] = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(doc); err != nil {
		return nil, err
	}
	if scripts == 0 {
		return nil, fmt.Errorf("index.html 缺少应用脚本引用")
	}
	result := make([]string, 0, len(refs))
	for ref := range refs {
		result = append(result, ref)
	}
	sort.Strings(result)
	return result, nil
}

func ValidateReferences(siteRoot, regionPath string) ([]string, error) {
	prefix := "/"
	if regionPath != "." && regionPath != "" {
		prefix += strings.Trim(regionPath, "/") + "/"
	}
	refs, err := References(filepath.Join(siteRoot, regionPath), prefix)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		info, err := os.Lstat(filepath.Join(siteRoot, filepath.FromSlash(ref)))
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("入口引用的本地资源不存在或不是普通文件: /%s", ref)
		}
	}
	return refs, nil
}
