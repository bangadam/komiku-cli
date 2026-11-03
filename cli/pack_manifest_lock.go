// pack_manifest_lock.go serializes manifest writers within and across processes.
package cli

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var manifestProcessLocks sync.Map

func acquireManifestLock(seriesDir string) (func(), error) {
	processLockValue, _ := manifestProcessLocks.LoadOrStore(seriesDir, &sync.Mutex{})
	processLock := processLockValue.(*sync.Mutex)
	processLock.Lock()
	processLocked := true
	defer func() {
		if processLocked {
			processLock.Unlock()
		}
	}()

	const lockName = ".pack.json.lock"
	root, err := os.OpenRoot(seriesDir)
	if err != nil {
		return nil, fmt.Errorf("open pack manifest lock root: %w", err)
	}
	before, inspectErr := root.Lstat(lockName)
	if inspectErr == nil && (before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular()) {
		root.Close()
		return nil, errors.New("pack manifest lock must be a regular non-symlink file")
	}
	if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
		root.Close()
		return nil, fmt.Errorf("inspect pack manifest lock: %w", inspectErr)
	}
	file, err := root.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("open pack manifest lock: %w", err)
	}
	opened, statErr := file.Stat()
	current, currentErr := root.Lstat(lockName)
	if statErr != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(current, opened) || (before != nil && !os.SameFile(before, opened)) {
		file.Close()
		root.Close()
		return nil, errors.New("pack manifest lock changed while opening")
	}
	unlock, err := lockManifestFile(file)
	if err != nil {
		file.Close()
		root.Close()
		return nil, err
	}
	processLocked = false
	return func() {
		unlock()
		_ = file.Close()
		_ = root.Close()
		processLock.Unlock()
	}, nil
}

const (
	maxPackManifestBytes    int64 = 4 << 20
	maxPackManifestMappings       = 1000
	maxPackManifestChapters       = 10_000
	maxPackChapterNumber          = 1_000_000
)
