package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type ResourceManifest struct {
	// APIVersion is a manifest schema marker. We don't enforce it
	// today but encourage `his.deploy/v1alpha1` so future readers
	// can branch on it.
	APIVersion string `yaml:"api_version"`
	// BundleVersion identifies which offline tar a manifest goes
	// with. Both the manifest and the tar declare the same string;
	// the install path refuses to run when they don't agree (see
	// offline.VerifyBundleVersion). Empty disables the check for
	// older bundles.
	BundleVersion      string              `yaml:"bundle_version"`
	ComponentResources map[string][]string `yaml:"component_resources"`
	Resources          []ResourceItem      `yaml:"resources"`
}

type ResourceItem struct {
	ID           string `yaml:"id"`
	Component    string `yaml:"component"`
	RelativePath string `yaml:"relative_path"`
	Type         string `yaml:"type"`
	Required     bool   `yaml:"required"`
	// SHA256 is the lower-case hex digest of the resource file. When
	// non-empty, resource_preflight reads the file and refuses to
	// proceed when the digest doesn't match. Empty preserves the
	// pre-hash behaviour ("file existence is enough").
	SHA256 string `yaml:"sha256"`
	// SizeBytes is informational. Not currently enforced — added so
	// future operators can spot truncated downloads at a glance.
	SizeBytes int64 `yaml:"size_bytes"`
}

func LoadResourceManifestFile(path string) (*ResourceManifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取资源清单失败: %w", err)
	}
	var manifest ResourceManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return nil, fmt.Errorf("解析资源清单失败: %w", err)
	}
	return &manifest, nil
}

func (m *ResourceManifest) MissingRequired(component string, resourceDir string) []ResourceItem {
	var missing []ResourceItem
	seen := map[string]bool{}
	for _, item := range m.Resources {
		if item.Component != component || !item.Required {
			continue
		}
		if mismatch, _ := resourceMismatchReason(item, resourceDir); mismatch {
			missing = append(missing, item)
			seen[item.RelativePath] = true
		}
	}
	itemsByPath := map[string]ResourceItem{}
	for _, item := range m.Resources {
		if item.RelativePath != "" {
			itemsByPath[item.RelativePath] = item
		}
	}
	for _, path := range m.ComponentResources[component] {
		if seen[path] {
			continue
		}
		item, ok := itemsByPath[path]
		if ok && !item.Required {
			continue
		}
		if !ok {
			item = ResourceItem{ID: component + "." + strings.ReplaceAll(path, "/", "."), Component: component, RelativePath: path, Required: true}
		}
		item.Component = component
		item.Required = true
		if mismatch, _ := resourceMismatchReason(item, resourceDir); !mismatch {
			continue
		}
		missing = append(missing, item)
		seen[path] = true
	}
	return missing
}

// MissingRequiredWithReasons is the verbose flavour of MissingRequired
// that also reports *why* each item was flagged. Useful for the
// resource_preflight error message and for the offline page so
// operators don't have to guess "file missing" vs "hash mismatch".
//
// Returned items are decorated: the same ResourceItem identity, plus
// a one-line reason ("missing" / "sha256 mismatch: want=... got=...").
type ResourceMismatch struct {
	Item   ResourceItem
	Reason string
}

func (m *ResourceManifest) MissingRequiredWithReasons(component string, resourceDir string) []ResourceMismatch {
	var out []ResourceMismatch
	seen := map[string]bool{}
	check := func(item ResourceItem) {
		mismatch, reason := resourceMismatchReason(item, resourceDir)
		if !mismatch {
			return
		}
		out = append(out, ResourceMismatch{Item: item, Reason: reason})
		seen[item.RelativePath] = true
	}
	for _, item := range m.Resources {
		if item.Component != component || !item.Required {
			continue
		}
		check(item)
	}
	itemsByPath := map[string]ResourceItem{}
	for _, item := range m.Resources {
		if item.RelativePath != "" {
			itemsByPath[item.RelativePath] = item
		}
	}
	for _, path := range m.ComponentResources[component] {
		if seen[path] {
			continue
		}
		item, ok := itemsByPath[path]
		if ok && !item.Required {
			continue
		}
		if !ok {
			item = ResourceItem{ID: component + "." + strings.ReplaceAll(path, "/", "."), Component: component, RelativePath: path, Required: true}
		}
		item.Component = component
		item.Required = true
		check(item)
	}
	return out
}

// resourceMismatchReason reports whether a resource is missing or
// content-mismatched, and a short reason string. SHA256 is only
// checked when the manifest provides a digest — empty digest skips
// content verification, preserving compatibility with older bundles.
func resourceMismatchReason(item ResourceItem, resourceDir string) (bool, string) {
	if item.RelativePath == "" || !safeRelativePath(item.RelativePath) {
		return true, "invalid relative_path"
	}
	full := filepath.Join(resourceDir, item.RelativePath)
	stat, err := os.Stat(full)
	if os.IsNotExist(err) {
		return true, "missing"
	}
	if err != nil {
		return true, "stat: " + err.Error()
	}
	// Directory entries declared in component_resources mean
	// "the directory should exist"; we don't hash directories.
	if stat.IsDir() {
		return false, ""
	}
	if item.SHA256 == "" {
		return false, ""
	}
	got, err := fileSHA256(full)
	if err != nil {
		return true, "hash: " + err.Error()
	}
	want := strings.ToLower(strings.TrimSpace(item.SHA256))
	if got != want {
		return true, "sha256 mismatch: want=" + want + " got=" + got
	}
	return false, ""
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func safeRelativePath(path string) bool {
	clean := filepath.Clean(path)
	return clean != "." && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
