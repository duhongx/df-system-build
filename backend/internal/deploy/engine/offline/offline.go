// Package offline encapsulates the operations needed to install or
// inspect an offline resource bundle. Both the CLI (`dfctl init`) and
// the Web platform need the same primitives — extracting a tar.gz,
// scanning the layout, validating against the resource manifest, and
// (since the safety hardening) checking disk space, doing atomic
// swap-in installs, and verifying the tar carries the bundle version
// the manifest expects.
package offline

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"df-build-server/internal/deploy/engine"
)

// BundleVersionFile is the canonical filename inside an offline tar
// that pairs the bundle with its manifest. The file holds a single
// version string (no newline required, but tolerated). Manifests
// declare the same value in `bundle_version`. Install refuses to
// proceed when the two disagree.
const BundleVersionFile = ".bundle-version"

// Options controls how Install behaves. Zero value works for the
// common case (atomic install, no clean, default size headroom).
type Options struct {
	// Clean replaces the entire target directory atomically. When
	// false, files in the tar are written on top of whatever's
	// already there — useful for incremental updates of one
	// component but slightly less safe.
	Clean bool
	// SizeHeadroomFactor scales the estimated extracted size by a
	// safety multiplier before checking disk space. Default 1.0.
	// Set higher when packages tend to compress well and you want
	// extra buffer.
	SizeHeadroomFactor float64
	// MinFreeBytes adds a fixed disk-space requirement on top of
	// the per-tar estimate, e.g. 1<<30 to keep at least 1 GiB free
	// for runtime use after extraction.
	MinFreeBytes int64
	// ExpectedBundleVersion, when non-empty, makes Install reject
	// any tar whose .bundle-version file doesn't match. Operators
	// pass the manifest's BundleVersion here so a misplaced tar
	// can't quietly desync from the manifest.
	ExpectedBundleVersion string
}

// Install extracts an offline tar.gz into targetDir with the safety
// guarantees configured by Options:
//
//   - Disk space check before any I/O happens
//   - Bundle version check against the tar's .bundle-version file
//     before we touch the live target directory
//   - Atomic swap-in (extract to staging → rename onto targetDir)
//     when Options.Clean is true; in-place merge otherwise
//
// On success, returns a Report with bundle metadata so callers can
// surface "you installed bundle vX.Y" to operators. On failure, the
// staging directory is removed and targetDir is left in its previous
// state (atomic semantics for Clean=true; partial merge possible for
// Clean=false but no worse than the legacy ExtractTarGz behaviour).
func Install(packagePath, targetDir string, opts Options) (*Report, error) {
	if err := checkDiskSpace(packagePath, targetDir, opts); err != nil {
		return nil, err
	}

	staging, err := os.MkdirTemp(filepath.Dir(targetDir), ".offline-staging-")
	if err != nil {
		return nil, fmt.Errorf("创建暂存目录失败: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := ExtractTarGz(packagePath, staging); err != nil {
		return nil, err
	}

	report := &Report{
		Target:        targetDir,
		Clean:         opts.Clean,
		ExtractedAt:   time.Now().UTC(),
		BundleVersion: readBundleVersion(staging),
	}
	if want := strings.TrimSpace(opts.ExpectedBundleVersion); want != "" {
		got := report.BundleVersion
		if got != want {
			return nil, fmt.Errorf(
				"离线包版本与清单不一致: 清单要求 %q,离线包是 %q (检查 manifest.yml::bundle_version 与 tar 内 .bundle-version 是否同步生成)",
				want, got)
		}
	}

	if err := promoteStaging(staging, targetDir, opts.Clean); err != nil {
		return nil, err
	}
	cleanup = false // promoteStaging consumed the staging dir
	return report, nil
}

// Report captures the outcome of a successful Install.
type Report struct {
	Target        string
	BundleVersion string
	Clean         bool
	ExtractedAt   time.Time
}

// promoteStaging moves the freshly extracted contents from staging
// into targetDir. Two strategies depending on Clean:
//
//   - clean=true: atomic. Move targetDir → backup, staging → targetDir.
//     Roll back on failure. Drop backup on success.
//   - clean=false: walk staging and copy/overlay each entry onto
//     targetDir. Operators get an in-place merge — newer files win
//     for shared paths, untouched files survive.
func promoteStaging(staging, targetDir string, clean bool) error {
	if clean {
		// Make sure the parent exists for the target so rename works.
		if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
			return fmt.Errorf("创建目标父目录失败: %w", err)
		}
		backup := targetDir + ".previous"
		_ = os.RemoveAll(backup)
		// Move existing target out of the way (if any).
		if _, err := os.Stat(targetDir); err == nil {
			if err := os.Rename(targetDir, backup); err != nil {
				return fmt.Errorf("备份现有目录失败: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("访问目标目录失败: %w", err)
		}
		if err := os.Rename(staging, targetDir); err != nil {
			// Roll back: restore previous target if we moved it.
			_ = os.RemoveAll(targetDir)
			if _, statErr := os.Stat(backup); statErr == nil {
				_ = os.Rename(backup, targetDir)
			}
			return fmt.Errorf("替换目标目录失败: %w", err)
		}
		_ = os.RemoveAll(backup)
		return nil
	}
	// In-place merge.
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}
	return mergeDir(staging, targetDir)
}

// mergeDir copies every entry from src to dst, preserving directory
// structure. Files in dst with the same path are overwritten; files
// in dst not mentioned by src are kept. Symlinks are recreated;
// the underlying ExtractTarGz already filtered unsafe ones.
func mergeDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			return os.Symlink(linkTarget, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode()|0o755)
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return copyFile(path, target, info.Mode())
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// readBundleVersion returns the trimmed contents of the tar's
// .bundle-version file, or "" if the file is absent. Bundles
// produced before this convention started simply have no version
// and the install path tolerates that.
func readBundleVersion(targetDir string) string {
	body, err := os.ReadFile(filepath.Join(targetDir, BundleVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// checkDiskSpace ensures the partition holding targetDir has enough
// free bytes for the upcoming extraction. We use a pessimistic
// estimate: tar.gz file size × 4 (typical text/binary mix compresses
// 2-3x) × Options.SizeHeadroomFactor, plus Options.MinFreeBytes.
//
// The check is best-effort. On platforms or filesystems where
// statfs is unavailable we skip silently rather than blocking
// installation; operators can always run df by hand if they want
// hard guarantees.
func checkDiskSpace(packagePath, targetDir string, opts Options) error {
	info, err := os.Stat(packagePath)
	if err != nil {
		return fmt.Errorf("访问离线包失败: %w", err)
	}
	headroom := opts.SizeHeadroomFactor
	if headroom <= 0 {
		headroom = 1.0
	}
	estimated := int64(float64(info.Size()*4) * headroom)
	estimated += opts.MinFreeBytes

	checkDir := targetDir
	for {
		parent := filepath.Dir(checkDir)
		if _, err := os.Stat(checkDir); err == nil {
			break
		}
		if parent == checkDir {
			break
		}
		checkDir = parent
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(checkDir, &stat); err != nil {
		return nil // best-effort, don't block install
	}
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	if avail < estimated {
		return fmt.Errorf(
			"磁盘空间不足: 离线包 %s 大小 %d 字节,估算解压需要 %d 字节,目标目录所在分区可用 %d 字节",
			packagePath, info.Size(), estimated, avail)
	}
	return nil
}

// ExtractTarGz expands a gzip-compressed tar archive at packagePath
// into targetDir. It refuses any entry whose cleaned path escapes
// targetDir (../ traversal) and ignores entries pointing outside the
// archive root through symlinks.
//
// Most callers should reach for Install() instead — it adds disk
// space checks, atomic swap-in, and bundle version verification
// around this function. ExtractTarGz remains exported because tests
// and the legacy CLI path still use it directly.
func ExtractTarGz(packagePath, targetDir string) error {
	f, err := os.Open(packagePath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("创建gzip读取器失败: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取tar条目失败: %w", err)
		}
		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			continue
		}
		target := filepath.Join(targetDir, cleanName)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)|0o755); err != nil {
				return fmt.Errorf("创建目录 %s 失败: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("创建父目录失败: %w", err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("创建文件 %s 失败: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("写入文件 %s 失败: %w", target, err)
			}
			out.Close()
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) || strings.HasPrefix(filepath.Clean(header.Linkname), "..") {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("创建父目录失败: %w", err)
			}
			os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("创建符号链接 %s 失败: %w", target, err)
			}
		}
	}
	return nil
}

// ScanResourceDirs walks the immediate children of targetDir and
// returns a map of "directory name" → file count. Empty directories are
// dropped so the caller can immediately tell whether each component
// shipped any payload.
func ScanResourceDirs(targetDir string) map[string]int {
	result := map[string]int{}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		count := countFiles(filepath.Join(targetDir, name))
		if count > 0 {
			result[name] = count
		}
	}
	return result
}

func countFiles(dir string) int {
	count := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	return count
}

// MissingManifestResources returns the resource items declared in
// manifestPath but absent under targetDir. Empty manifestPath or a
// missing manifest is treated as "nothing to check"; this keeps the
// behaviour matching the legacy CLI.
func MissingManifestResources(manifestPath, targetDir string) ([]engine.ResourceItem, error) {
	if manifestPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("无法访问资源清单 %s: %w", manifestPath, err)
	}
	manifest, err := engine.LoadResourceManifestFile(manifestPath)
	if err != nil {
		return nil, err
	}
	components := map[string]bool{}
	for component := range manifest.ComponentResources {
		components[component] = true
	}
	for _, item := range manifest.Resources {
		if item.Component != "" {
			components[item.Component] = true
		}
	}
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	var missing []engine.ResourceItem
	for _, component := range names {
		missing = append(missing, manifest.MissingRequired(component, targetDir)...)
	}
	return missing, nil
}
