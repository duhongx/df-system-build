// Package cli implements the deployment-management CLI subcommands
// (`df-build-server deploy verify` and `deploy manifest gen`), ported from
// his-deploy's standalone CLI. They reproduce the offline-bundle integrity
// checks outside the Web UI.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"df-build-server/internal/deploy/engine/offline"

	"gopkg.in/yaml.v3"
)

type manifestDoc struct {
	APIVersion         string                `yaml:"api_version"`
	GeneratedAt        string                `yaml:"generated_at,omitempty"`
	ResourceRoot       string                `yaml:"resource_root,omitempty"`
	BundleVersion      string                `yaml:"bundle_version,omitempty"`
	ComponentResources map[string][]string   `yaml:"component_resources,omitempty"`
	Resources          []manifestResourceDoc `yaml:"resources"`
}

type manifestResourceDoc struct {
	ID           string `yaml:"id"`
	Component    string `yaml:"component"`
	RelativePath string `yaml:"relative_path"`
	Type         string `yaml:"type,omitempty"`
	Required     bool   `yaml:"required"`
	SHA256       string `yaml:"sha256,omitempty"`
	SizeBytes    int64  `yaml:"size_bytes,omitempty"`
}

// ManifestGenResult summarizes a manifest gen run.
type ManifestGenResult struct {
	Files      int
	Components int
	Output     string
}

// ManifestGen walks resourceDir, computes sha256 + size per file, groups by
// top-level directory (= component), and writes manifest.yml. When
// bundleVersion is non-empty it is embedded and written to .bundle-version.
func ManifestGen(resourceDir, output, bundleVersion string, exclusions []string) (*ManifestGenResult, error) {
	if strings.TrimSpace(resourceDir) == "" {
		return nil, fmt.Errorf("resource dir is required")
	}
	if output == "" {
		output = filepath.Join(filepath.Dir(filepath.Clean(resourceDir)), "manifest.yml")
	}
	items, perComponent, err := scanResources(resourceDir, exclusions)
	if err != nil {
		return nil, err
	}
	doc := manifestDoc{
		APIVersion:         "v1",
		GeneratedAt:        time.Now().UTC().Format(time.RFC3339),
		ResourceRoot:       resourceDir,
		BundleVersion:      bundleVersion,
		ComponentResources: perComponent,
		Resources:          items,
	}
	if err := writeManifest(output, doc); err != nil {
		return nil, err
	}
	if bundleVersion != "" {
		bv := filepath.Join(resourceDir, offline.BundleVersionFile)
		if err := os.WriteFile(bv, []byte(bundleVersion+"\n"), 0o644); err != nil {
			return nil, fmt.Errorf("write .bundle-version: %w", err)
		}
	}
	return &ManifestGenResult{Files: len(items), Components: len(perComponent), Output: output}, nil
}

// VerifyResult is the outcome of a verify run.
type VerifyResult struct {
	Missing []string
	OK      bool
}

// Verify checks that every resource declared in manifestPath is present under
// resourceDir. A non-empty Missing list means verification failed.
func Verify(resourceDir, manifestPath string) (*VerifyResult, error) {
	missing, err := offline.MissingManifestResources(manifestPath, resourceDir)
	if err != nil {
		return nil, err
	}
	res := &VerifyResult{OK: len(missing) == 0}
	for _, m := range missing {
		res.Missing = append(res.Missing, m.RelativePath)
	}
	sort.Strings(res.Missing)
	return res, nil
}

func scanResources(resourceDir string, exclusions []string) ([]manifestResourceDoc, map[string][]string, error) {
	items := []manifestResourceDoc{}
	perComponent := map[string][]string{}
	err := filepath.Walk(resourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == resourceDir {
			return nil
		}
		rel, err := filepath.Rel(resourceDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, ex := range exclusions {
			if rel == ex || strings.HasPrefix(rel, ex+"/") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		comp := topLevelComponent(rel)
		if comp == "" {
			return nil
		}
		hash, hashErr := fileSHA256(path)
		if hashErr != nil {
			return fmt.Errorf("hash %s: %w", rel, hashErr)
		}
		items = append(items, manifestResourceDoc{
			ID:           buildResourceID(rel),
			Component:    comp,
			RelativePath: rel,
			Type:         classifyResourceType(rel, info),
			Required:     true,
			SHA256:       hash,
			SizeBytes:    info.Size(),
		})
		perComponent[comp] = append(perComponent[comp], rel)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for k := range perComponent {
		sort.Strings(perComponent[k])
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RelativePath < items[j].RelativePath })
	return items, perComponent, nil
}

func topLevelComponent(rel string) string {
	parts := strings.SplitN(rel, "/", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func classifyResourceType(rel string, info os.FileInfo) string {
	switch {
	case strings.HasSuffix(rel, ".tmpl"), strings.HasSuffix(rel, ".j2"):
		return "template"
	case strings.Contains(rel, "/images/") && strings.HasSuffix(rel, ".tar"):
		return "image"
	case strings.HasSuffix(rel, ".tar"), strings.HasSuffix(rel, ".tar.gz"), strings.HasSuffix(rel, ".tgz"), strings.HasSuffix(rel, ".zip"):
		return "archive"
	case info.Mode().Perm()&0o111 != 0:
		return "binary"
	default:
		return "asset"
	}
}

func buildResourceID(rel string) string {
	id := strings.ReplaceAll(rel, "/", ".")
	return strings.ReplaceAll(id, "-", ".")
}

func writeManifest(path string, doc manifestDoc) error {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
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
