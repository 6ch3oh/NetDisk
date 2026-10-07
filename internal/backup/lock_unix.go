//go:build !windows

package backup

import (
	"errors"
	"golang.org/x/sys/unix"
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
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another backup process owns this state")
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
}
