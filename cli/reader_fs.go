// reader_fs.go confines all reader paths with os.Root and performs atomic cache writes.
package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bangadam/komiku-cli/store"
)

// readerFS confines all untrusted descendants with os.Root, in addition to
// rejecting symlinks. Only the user-selected output root may resolve symlinks
// (important for macOS /var and user-configured library mounts).
type readerFS struct {
	root *os.Root
	base string
}

func openReaderFS(output, series string, create bool) (*readerFS, error) {
	if series == "" || series == "." || series == ".." || strings.ContainsAny(series, "/\\\x00") {
		return nil, errors.New("invalid reader root component")
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(absolute, 0o755); err != nil {
			return nil, err
		}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if errors.Is(err, os.ErrNotExist) && !create {
		return &readerFS{base: filepath.Join(absolute, series)}, nil
	}
	if err != nil {
		return nil, err
	}
	outputRoot, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, err
	}
	defer outputRoot.Close()
	info, err := lstatRealRootPath(outputRoot, series)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := outputRoot.Mkdir(series, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err = lstatRealRootPath(outputRoot, series)
	}
	fs := &readerFS{base: filepath.Join(resolved, series)}
	if errors.Is(err, os.ErrNotExist) && !create {
		return fs, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("series path is not a real directory")
	}
	fs.root, err = outputRoot.OpenRoot(series)
	if err != nil {
		return nil, err
	}
	opened, err := fs.root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		fs.close()
		return nil, errors.New("series directory changed while opening")
	}
	return fs, nil
}

func (fs *readerFS) close() {
	if fs.root != nil {
		fs.root.Close()
	}
}

func (fs *readerFS) open(relative string) (*os.File, error) {
	if fs.root == nil {
		return nil, os.ErrNotExist
	}
	info, err := lstatRealRootPath(fs.root, relative)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", relative)
	}
	file, err := fs.root.Open(relative)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, fmt.Errorf("%s changed while opening", relative)
	}
	return file, nil
}

func (fs *readerFS) entries(relative string) ([]os.DirEntry, error) {
	if fs.root == nil {
		return nil, nil
	}
	var info os.FileInfo
	var err error
	if relative == "." {
		info, err = fs.root.Stat(".")
	} else {
		info, err = lstatRealRootPath(fs.root, relative)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", relative)
	}
	dir, err := fs.root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("reader directory changed while opening")
	}
	entries, err := dir.ReadDir(maxReaderChapters*4 + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxReaderChapters*4 {
		return nil, errors.New("reader directory entry limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func (fs *readerFS) mkdir(relative string) error {
	current := ""
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return errors.New("invalid reader directory")
		}
		current = filepath.Join(current, part)
		info, err := lstatRealRootPath(fs.root, current)
		if errors.Is(err, os.ErrNotExist) {
			if err := fs.root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = lstatRealRootPath(fs.root, current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
	}
	return nil
}

func (fs *readerFS) readJSON(relative string, value any) error {
	file, err := fs.open(relative)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReaderMetadata+1))
	if err != nil {
		return err
	}
	if len(data) > maxReaderMetadata {
		return errors.New("reader metadata exceeds byte limit")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func (fs *readerFS) done() (map[float64]bool, error) {
	var state store.State
	err := fs.readJSON(".state.json", &state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read download state: %w", err)
	}
	result := make(map[float64]bool, len(state.Done))
	for _, number := range state.Done {
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return nil, errors.New("invalid downloaded chapter number")
		}
		result[number] = true
	}
	return result, nil
}

// atomicFile never follows an existing target and never removes other data.
// os.Root confines both the temporary file and rename even during path races.
func (fs *readerFS) atomicFile(relative string, write func(*os.File) error) error {
	parent := filepath.Dir(relative)
	info, err := lstatRealRootPath(fs.root, parent)
	if err != nil {
		return err
	}
	// Anchor to the cache directory as well as the series root, so replacing
	// a cache parent with an in-root symlink cannot redirect a write to library.
	confined, err := fs.root.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer confined.Close()
	opened, err := confined.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("reader cache changed while opening")
	}
	leaf := filepath.Base(relative)
	if info, err := lstatRealRootPath(confined, leaf); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("reader target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf(".reader-%x.part", nonce)
	file, err := confined.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer confined.Remove(tmp)
	defer file.Close()
	if err := write(file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := confined.Rename(tmp, leaf); err != nil {
		return err
	}
	dir, err := confined.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (fs *readerFS) saveManifest(dir string, manifest readerManifest) error {
	if err := validateReaderManifest(manifest, manifest.SeriesURL, dir); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(data) > maxReaderMetadata {
		return errors.New("reader metadata exceeds byte limit")
	}
	return fs.atomicFile(filepath.Join(dir, "manifest.json"), func(file *os.File) error {
		_, err := file.Write(append(data, '\n'))
		return err
	})
}

func (fs *readerFS) saveImage(ctx context.Context, dir string, page readerManifestPage, extension string, body io.Reader) (readerManifestPage, error) {
	if !readerImageExtension(extension) {
		return page, errors.New("unsupported reader image extension")
	}
	page.File = readerPageStem(page) + strings.ToLower(extension)
	err := fs.atomicFile(filepath.Join(dir, page.File), func(file *os.File) error {
		n, err := io.Copy(file, io.LimitReader(readerContextReader{ctx, body}, maxReaderImage+1))
		if err != nil {
			return err
		}
		if n > maxReaderImage {
			return errors.New("reader image exceeds byte limit")
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		page.Bytes, page.SHA256, err = readerImageInfo(ctx, file)
		return err
	})
	return page, err
}
