package opsremote

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
)

//go:embed runners/*
var runners embed.FS

func Runner(arch string) ([]byte, string, error) {
	if arch != "amd64" && arch != "arm64" {
		return nil, "", fmt.Errorf("不支持的服务器架构 %s", arch)
	}
	data, err := runners.ReadFile("runners/linux-" + arch + ".gz")
	if err != nil {
		return nil, "", fmt.Errorf("程序未打包 Linux %s 巡检执行器，请使用 scripts/build_dfops.sh 编译", arch)
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	binary, err := io.ReadAll(io.LimitReader(r, 32<<20))
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(binary)
	return binary, fmt.Sprintf("%x", sum), nil
}
