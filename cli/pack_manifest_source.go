// pack_manifest_source.go resolves and confines manifest pack sources inside the series directory.
package cli

import (
	"errors"
	"fmt"
	"github.com/bangadam/komiku-cli/store"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func canonicalSeriesRoot(seriesDir string) (string, error) {
	if seriesDir == "" {
		return "", errors.New("series directory is empty")
	}
	info, err := os.Stat(seriesDir)
	if err != nil {
		return "", fmt.Errorf("open series directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("series path is not a directory: %s", seriesDir)
	}
	root, err := filepath.EvalSymlinks(seriesDir)
	if err != nil {
		return "", fmt.Errorf("resolve series directory: %w", err)
	}
	// Always hand back an absolute root: source paths reach this code either
	// relative to the series root or relative to the process working
	// directory, and only an absolute root lets filepath.Rel reconcile them.
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve series directory: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func lstatRealRootPath(root *os.Root, relative string) (os.FileInfo, error) {
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("source path escapes series root")
	}
	parts := strings.Split(clean, string(filepath.Separator))
	current := ""
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("source path is malformed")
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("source path component %s is a symlink", current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("source path component %s is not a directory", current)
		}
		if index == len(parts)-1 {
			return info, nil
		}
	}
	return nil, errors.New("source path is empty")
}

func validatePackSource(root, source string) (string, int, error) {
	relative, err := sourceRelativeToRoot(root, source)
	if err != nil {
		return "", 0, err
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return "", 0, fmt.Errorf("open series root: %w", err)
	}
	defer confined.Close()
	info, err := lstatRealRootPath(confined, relative)
	if err != nil {
		return "", 0, fmt.Errorf("inspect source directory: %w", err)
	}
	if !info.IsDir() {
		return "", 0, errors.New("source directory must be a real directory, not a symlink")
	}
	dir, err := confined.Open(relative)
	if err != nil {
		return "", 0, fmt.Errorf("open source directory: %w", err)
	}
	defer dir.Close()
	openedInfo, err := dir.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return "", 0, errors.New("source directory changed while opening")
	}
	pages := make(map[int]bool)
	for {
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return "", 0, fmt.Errorf("source entry %s is a symlink", entry.Name())
			}
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension != ".jpg" && extension != ".jpeg" && extension != ".png" && extension != ".webp" {
				continue
			}
			number, parseErr := strconv.Atoi(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
			if parseErr != nil || number <= 0 {
				continue
			}
			if number > store.MaxChapterPages || len(pages) >= store.MaxChapterPages {
				return "", 0, fmt.Errorf("source page number exceeds limit %d", store.MaxChapterPages)
			}
			if pages[number] {
				return "", 0, fmt.Errorf("duplicate source page %03d", number)
			}
			pages[number] = true
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, fmt.Errorf("read source directory: %w", readErr)
		}
	}
	if len(pages) == 0 {
		return "", 0, errors.New("chapter has no valid image pages")
	}
	for number := 1; number <= len(pages); number++ {
		if !pages[number] {
			return "", 0, fmt.Errorf("chapter is missing page %03d", number)
		}
	}
	return relative, len(pages), nil
}

func sourceRelativeToRoot(root, source string) (string, error) {
	if source == "" || strings.ContainsAny(source, "\x00\\") {
		return "", errors.New("source directory is empty or malformed")
	}
	var relative string
	if filepath.IsAbs(source) {
		providedInfo, err := os.Lstat(filepath.Clean(source))
		if err != nil {
			return "", fmt.Errorf("inspect source directory: %w", err)
		}
		if providedInfo.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("source directory is a symlink")
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(source))
		if err != nil {
			return "", fmt.Errorf("resolve source directory: %w", err)
		}
		relative, err = filepath.Rel(root, resolved)
		if err != nil {
			return "", fmt.Errorf("resolve source directory: %w", err)
		}
	} else {
		candidate := filepath.Clean(filepath.FromSlash(source))
		// Sources arrive either as series-root-relative paths ("chapter-003")
		// or as paths relative to the process working directory when the
		// configured output root is itself relative ("versus/chapter-003").
		// Prefer the interpretation that actually exists under root.
		if _, err := os.Lstat(filepath.Join(root, candidate)); err == nil {
			relative = candidate
		} else if workingDir, wdErr := os.Getwd(); wdErr == nil {
			absolute := filepath.Join(workingDir, candidate)
			if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
				if rel, relErr := filepath.Rel(root, resolved); relErr == nil {
					relative = rel
				} else {
					relative = candidate
				}
			} else {
				relative = candidate
			}
		} else {
			relative = candidate
		}
	}
	if relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative != filepath.Clean(relative) {
		return "", fmt.Errorf("source directory escapes series root: %s", source)
	}
	return relative, nil
}
