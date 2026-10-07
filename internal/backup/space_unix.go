//go:build !windows

package backup

import "golang.org/x/sys/unix"

func diskFree(path string) (uint64, error) {
	var s unix.Statfs_t
	err := unix.Statfs(path, &s)
	return s.Bavail * uint64(s.Bsize), err
}
