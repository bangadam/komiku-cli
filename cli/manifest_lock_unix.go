//go:build unix

package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func lockManifestFile(file *os.File) (func(), error) {
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
		if err == nil {
			return func() {
				unlock := unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 1}
				_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &unlock)
			}, nil
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EACCES) {
			return nil, fmt.Errorf("lock pack manifest: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("pack manifest is locked by another process: %s", file.Name())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
