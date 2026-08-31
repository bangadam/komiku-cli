package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bangadam/komiku-cli/komiku"
)

const (
	MinResumeSize   int64 = 10 * 1024
	MaxChapterPages       = 10_000
)

var atomicRename = os.Rename

type State struct {
	Done []float64 `json:"done"`
}

type SeriesStore struct {
	Root      string
	Series    string
	seriesDir string
	statePath string

	mu    sync.Mutex
	state State
}

func Open(root, series string) (*SeriesStore, error) {
	if root == "" {
		return nil, errors.New("output root is empty")
	}
	if series == "" || series == "." || series == ".." || strings.ContainsAny(series, "/\\\x00") {
		return nil, fmt.Errorf("invalid series directory %q", series)
	}
	seriesDir := filepath.Join(root, series)
	if err := os.MkdirAll(seriesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create series directory: %w", err)
	}
	s := &SeriesStore{Root: root, Series: series, seriesDir: seriesDir, statePath: filepath.Join(seriesDir, ".state.json")}
	if err := s.loadState(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SeriesStore) Dir() string { return s.seriesDir }

func (s *SeriesStore) IsDone(chapter float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, done := range s.state.Done {
		if done == chapter {
			return true
		}
	}
	return false
}

func (s *SeriesStore) MarkDone(chapter float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, done := range s.state.Done {
		if done == chapter {
			return nil
		}
	}
	next := State{Done: append(append([]float64(nil), s.state.Done...), chapter)}
	sort.Float64s(next.Done)
	if err := writeJSONAtomic(s.statePath, next, 0o644); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	s.state = next
	return nil
}

func (s *SeriesStore) loadState() error {
	data, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	return nil
}

func (s *SeriesStore) ChapterDir(display, rawDisambiguator string, volume int, flat bool) (string, error) {
	chapter, err := chapterName(display, rawDisambiguator)
	if err != nil {
		return "", err
	}
	var dir string
	if flat {
		dir = filepath.Join(s.seriesDir, chapter)
	} else {
		if volume <= 0 {
			return "", errors.New("volume is required unless flat mode is enabled")
		}
		dir = filepath.Join(s.seriesDir, fmt.Sprintf("vol-%02d", volume), chapter)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func chapterName(display, rawDisambiguator string) (string, error) {
	chapter := "chapter-" + FormatChapter(display)
	if rawDisambiguator == "" {
		return chapter, nil
	}
	if strings.ContainsAny(rawDisambiguator, "/\\\x00") {
		return "", fmt.Errorf("invalid raw chapter identity %q", rawDisambiguator)
	}
	return chapter + "-raw-" + rawDisambiguator, nil
}

func (s *SeriesStore) MaterializeDone(display, rawDisambiguator string, volume int, flat bool) (bool, error) {
	name, err := chapterName(display, rawDisambiguator)
	if err != nil {
		return false, err
	}
	var target string
	if flat {
		target = filepath.Join(s.seriesDir, name)
	} else if volume > 0 {
		target = filepath.Join(s.seriesDir, fmt.Sprintf("vol-%02d", volume), name)
	} else {
		return false, errors.New("volume is required unless flat mode is enabled")
	}
	if hasImagePage(target) {
		return true, nil
	}
	var source string
	err = filepath.WalkDir(s.seriesDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == name && path != target && hasImagePage(path) {
			source = path
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil || source == "" {
		return false, err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return false, err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !validImageFile(filepath.Join(source, entry.Name())) {
			continue
		}
		src, dst := filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())
		if err := os.Link(src, dst); err != nil && !errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("materialize completed page %s: %w", entry.Name(), err)
		}
	}
	return hasImagePage(target), nil
}

func hasImagePage(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && validImageFile(filepath.Join(dir, entry.Name())) {
			return true
		}
	}
	return false
}
func CountChapterPages(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read chapter pages: %w", err)
	}
	pages := make(map[int]string, min(len(entries), MaxChapterPages))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		extension := filepath.Ext(entry.Name())
		number, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), extension))
		if err != nil || number <= 0 || extension == "" {
			continue
		}
		filename := filepath.Join(dir, entry.Name())
		if !validImageFile(filename) {
			return 0, fmt.Errorf("page %03d is not a valid image", number)
		}
		if previous, exists := pages[number]; exists {
			return 0, fmt.Errorf("duplicate page %03d (%s and %s)", number, previous, entry.Name())
		}
		pages[number] = entry.Name()
		if len(pages) > MaxChapterPages || number > MaxChapterPages {
			return 0, fmt.Errorf("chapter page number exceeds limit %d", MaxChapterPages)
		}
	}
	if len(pages) == 0 {
		return 0, errors.New("chapter has no valid image pages")
	}
	numbers := make([]int, 0, len(pages))
	for number := range pages {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	for index, number := range numbers {
		expected := index + 1
		if number != expected {
			return 0, fmt.Errorf("chapter is missing page %03d", expected)
		}
	}
	return len(numbers), nil
}

func FormatChapter(display string) string {
	parts := strings.SplitN(display, ".", 2)
	whole := strings.TrimLeft(parts[0], "0")
	if whole == "" {
		whole = "0"
	}
	if len(whole) < 3 {
		whole = strings.Repeat("0", 3-len(whole)) + whole
	}
	if len(parts) == 2 {
		return whole + "." + parts[1]
	}
	return whole
}

func ExistingPage(dir string, page int) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	prefix := fmt.Sprintf("%03d.", page)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		filename := filepath.Join(dir, entry.Name())
		if ValidResumeFile(filename) {
			return filename, true
		}
	}
	return "", false
}

func ValidResumeFile(filename string) bool {
	info, err := os.Stat(filename)
	return err == nil && info.Size() > MinResumeSize && validImageFile(filename)
}

func validImageFile(filename string) bool {
	file, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer file.Close()
	var header [12]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	_, valid := komiku.DetectImage(header[:n])
	return valid
}

func WritePage(dir string, page int, extension string, body io.Reader) error {
	if extension == "" || extension[0] != '.' || strings.ContainsAny(extension, `/\\\x00`) {
		return fmt.Errorf("invalid image extension %q", extension)
	}
	final := filepath.Join(dir, fmt.Sprintf("%03d%s", page, extension))
	tmp, err := os.CreateTemp(dir, fmt.Sprintf(".%03d-*.part", page))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, body); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = atomicRename(tmpName, final); err != nil {
		return err
	}
	entries, _ := os.ReadDir(dir)
	prefix := fmt.Sprintf("%03d.", page)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && filepath.Join(dir, name) != final {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}

func writeJSONAtomic(filename string, value any, mode os.FileMode) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(filename)
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return atomicRename(tmpName, filename)
}

func WriteJSONAtomic(filename string, value any) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	return writeJSONAtomic(filename, value, 0o644)
}
