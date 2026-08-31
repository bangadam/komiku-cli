//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func lockManifestFile(file *os.File) (func(), error) {
	var overlapped windows.Overlapped
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped) }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("lock pack manifest: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("pack manifest is locked by another process: %s", file.Name())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
