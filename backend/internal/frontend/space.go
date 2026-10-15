package frontend

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func RequireSpace(dir string, bytes uint64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return fmt.Errorf("无法读取构建目录剩余容量: %w", err)
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	const reserve = 256 << 20
	if bytes > available || available-bytes < reserve {
		return fmt.Errorf("构建空间不足：需要至少 %d MiB 并保留 256 MiB，剩余 %d MiB", bytes>>20, available>>20)
	}
	return nil
}
