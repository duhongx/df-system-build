package frontend

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func ExportPodSite(ctx context.Context, cs kubernetes.Interface, cfg *rest.Config, ns, pod, container, destination string) error {
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(ns).SubResource("exec").Param("container", container).
		Param("command", "tar").Param("command", "cf").Param("command", "-").Param("command", "-C").Param("command", "/usr/share/nginx/html").Param("command", ".").Param("stdout", "true").Param("stderr", "true")
	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	copyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	var diagnostics diagnosticBuffer
	go func() {
		err := executor.StreamWithContext(copyCtx, remotecommand.StreamOptions{Stdout: pw, Stderr: &diagnostics})
		_ = pw.CloseWithError(err)
		done <- err
	}()
	extractErr := untarSite(pr, destination)
	if extractErr == nil {
		_, extractErr = io.Copy(io.Discard, pr)
	}
	_ = pr.Close()
	if extractErr != nil {
		cancel()
	}
	streamErr := <-done
	if extractErr != nil || streamErr != nil {
		return fmt.Errorf("读取 Pod %s 站点失败: %v；%v；%s", pod, extractErr, streamErr, diagnostics.String())
	}
	return nil
}

func ProbePodHTTP(ctx context.Context, cs kubernetes.Interface, cfg *rest.Config, ns, pod, container, urlPath string) ([]byte, string, error) {
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(ns).SubResource("exec").Param("container", container).
		Param("command", "wget").Param("command", "-S").Param("command", "-O").Param("command", "-").Param("command", "http://127.0.0.1"+urlPath).Param("stdout", "true").Param("stderr", "true")
	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return nil, "", err
	}
	var stdout bytes.Buffer
	var stderr diagnosticBuffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})
	return stdout.Bytes(), stderr.String(), err
}

type diagnosticBuffer struct{ data bytes.Buffer }

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 65536 - b.data.Len()
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, _ = b.data.Write(p)
	return n, nil
}
func (b *diagnosticBuffer) String() string { return b.data.String() }
func untarSite(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	var total int64
	count := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		count++
		if count > 100000 || header.Size < 0 || header.Size > 4<<30 || total > (4<<30)-header.Size {
			return fmt.Errorf("Pod 站点超过容量限制")
		}
		total += header.Size
		file := filepath.Join(dest, header.Name)
		rel, err := filepath.Rel(dest, file)
		if err != nil || filepath.IsAbs(header.Name) || rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("Pod 站点包含非法路径")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(file, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("Pod 站点不允许链接或特殊文件")
		}
	}
}

var httpStatusLine = regexp.MustCompile(`(?m)^\s*HTTP/\S+\s+(\d{3})\b`)

func HTTPStatus(headers string) int {
	matches := httpStatusLine.FindAllStringSubmatch(headers, -1)
	if len(matches) == 0 {
		return 0
	}
	status, _ := strconv.Atoi(matches[len(matches)-1][1])
	return status
}

// Batch old-URL checks in one exec stream. Only a temporary response file is
// written; published files remain read-only. Input paths are URL-escaped data.
func VerifyHTTPFiles(ctx context.Context, cs kubernetes.Interface, cfg *rest.Config, ns, pod, container string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	var input strings.Builder
	for name, hash := range files {
		u := url.URL{Path: "/" + name}
		fmt.Fprintf(&input, "%s %s\n", hash, u.EscapedPath())
	}
	script := `f=$(mktemp) || exit 1; trap 'rm -f "$f"' EXIT
while read -r expected path; do
 wget -q -O "$f" "http://127.0.0.1$path" || { echo "HTTP failed: $path" >&2; exit 1; }
 actual=$(sha256sum "$f") || exit 1
 [ "${actual%% *}" = "$expected" ] || { echo "HTTP checksum mismatch: $path" >&2; exit 1; }
done`
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(ns).SubResource("exec").Param("container", container).
		Param("command", "sh").Param("command", "-c").Param("command", script).Param("stdin", "true").Param("stdout", "true").Param("stderr", "true")
	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	var diagnostics diagnosticBuffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: strings.NewReader(input.String()), Stdout: io.Discard, Stderr: &diagnostics}); err != nil {
		return fmt.Errorf("历史资源 HTTP 验证失败: %w；%s", err, diagnostics.String())
	}
	return nil
}
