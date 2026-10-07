//go:build windows

package backup

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

func lockState(name string) (func(), error) {
	if info, err := os.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("invalid state lock file")
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		f.Close()
		return nil, errors.New("another backup process owns this state")
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped); f.Close() }, nil
}
