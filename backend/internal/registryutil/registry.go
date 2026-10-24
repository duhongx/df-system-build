package registryutil

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxCACertificateFileSize = 256 * 1024

// Endpoint parses a registry address entered as either host[:port],
// http://host[:port], or https://host[:port]. Addresses without a scheme
// retain the project's historical HTTP behavior.
func Endpoint(address string) (host, baseURL string, err error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", "", fmt.Errorf("镜像仓库地址未填写")
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", "", fmt.Errorf("镜像仓库地址格式无效，请填写 host[:port] 或 http(s)://host[:port]")
	}
	return u.Host, u.Scheme + "://" + u.Host, nil
}

// HTTPClient creates a client that trusts system roots and, when provided,
// an additional PEM encoded registry CA certificate bundle.
func HTTPClient(caPEM string, timeout time.Duration) (*http.Client, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	} else {
		transport = transport.Clone()
	}
	if strings.TrimSpace(caPEM) != "" {
		if err := ValidateCAPEM([]byte(caPEM)); err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		roots.AppendCertsFromPEM([]byte(caPEM))
		tlsConfig := &tls.Config{}
		if transport.TLSClientConfig != nil {
			tlsConfig = transport.TLSClientConfig.Clone()
		}
		tlsConfig.RootCAs = roots
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// ReadCAPEM reads a bounded file containing only CA certificates in PEM form.
func ReadCAPEM(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("证书路径必须是服务器上的绝对路径")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("打开证书文件失败: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取证书文件信息失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("证书路径必须指向普通文件")
	}
	if info.Size() > maxCACertificateFileSize {
		return nil, fmt.Errorf("证书文件不能超过 %d KB", maxCACertificateFileSize/1024)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxCACertificateFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取证书文件失败: %w", err)
	}
	if len(content) > maxCACertificateFileSize {
		return nil, fmt.Errorf("证书文件不能超过 %d KB", maxCACertificateFileSize/1024)
	}
	if err := ValidateCAPEM(content); err != nil {
		return nil, err
	}
	return content, nil
}

// ValidateCAPEM accepts one or more PEM encoded CA certificates and rejects
// private keys, server leaf certificates, and unrelated PEM blocks.
func ValidateCAPEM(content []byte) error {
	rest := bytes.TrimSpace(content)
	count := 0
	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return fmt.Errorf("文件必须只包含 PEM 格式的 CA 证书")
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return fmt.Errorf("文件必须只包含 PEM 格式的 CA 证书")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA {
			return fmt.Errorf("PEM 文件中包含无效证书或非 CA 证书")
		}
		count++
		rest = bytes.TrimSpace(remaining)
	}
	if count == 0 {
		return fmt.Errorf("文件中没有 CA 证书")
	}
	return nil
}
