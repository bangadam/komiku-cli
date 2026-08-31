package pack

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var atomicRootRename = func(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
}

var afterChapterSourceInspect = func(string) {}

const (
	MaxPageBytes      int64 = 64 << 20
	MaxVolumeBytes    int64 = 16 << 30
	MaxImageDimension       = 30_000
	MaxImagePixels    int64 = 100_000_000
)

type Preset string

const (
	Medium Preset = "medium"
	Small  Preset = "small"
	Tiny   Preset = "tiny"
	Raw    Preset = "raw"
)

type Warning struct {
	Entry  string
	Source string
	Err    error
}

func (w Warning) Error() string {
	return fmt.Sprintf("%s: conversion failed for %s; copied original: %v", w.Entry, w.Source, w.Err)
}

type Result struct {
	Path     string
	Preset   Preset
	Warnings []Warning
}

type presetSettings struct {
	longestSide int
	quality     int
}

func (p Preset) settings() (presetSettings, error) {
	switch p {
	case Medium:
		return presetSettings{longestSide: 1600, quality: 72}, nil
	case Small:
		return presetSettings{longestSide: 1400, quality: 65}, nil
	case Tiny:
		return presetSettings{longestSide: 1200, quality: 60}, nil
	case Raw:
		return presetSettings{}, nil
	default:
		return presetSettings{}, fmt.Errorf("unknown pack preset %q", p)
	}
}

type Chapter struct {
	Number        float64
	Display       string
	Dir           string
	ExpectedPages int
}

type Volume struct {
	SeriesDir  string
	SourceRoot string
	Series     string
	Number     int
	Chapters   []Chapter
}

type page struct {
	number int
	path   string
	ext    string
	info   os.FileInfo
	size   int64
}

// RawVolume copies original image bytes into a ZIP_STORED CBZ and atomically publishes it.
func RawVolume(volume Volume) (string, error) {
	result, err := PackVolume(context.Background(), volume, Raw)
	return result.Path, err
}

// PackVolume packs one volume using the selected Kindle preset.
func PackVolume(ctx context.Context, volume Volume, preset Preset) (Result, error) {
	settings, err := preset.settings()
	if err != nil {
		return Result{}, err
	}
	if volume.SeriesDir == "" {
		return Result{}, errors.New("series directory is empty")
	}
	outputInfo, err := os.Lstat(volume.SeriesDir)
	if err != nil {
		return Result{}, fmt.Errorf("inspect series directory: %w", err)
	}
	if outputInfo.Mode()&os.ModeSymlink != 0 || !outputInfo.IsDir() {
		return Result{}, errors.New("series directory must be a real directory, not a symlink")
	}
	outputRoot, err := os.OpenRoot(volume.SeriesDir)
	if err != nil {
		return Result{}, fmt.Errorf("open series directory root: %w", err)
	}
	defer outputRoot.Close()
	openedOutputInfo, err := outputRoot.Stat(".")
	if err != nil || !os.SameFile(outputInfo, openedOutputInfo) {
		return Result{}, errors.New("series directory changed while opening")
	}
	if err := validateArchiveLeaf(volume.Series); err != nil {
		return Result{}, err
	}
	if volume.Number <= 0 || volume.Number > 1000 {
		return Result{}, fmt.Errorf("invalid volume number %d", volume.Number)
	}
	var sourceRoot *os.Root
	if volume.SourceRoot != "" {
		sourceInfo, err := os.Lstat(volume.SourceRoot)
		if err != nil {
			return Result{}, fmt.Errorf("inspect source root: %w", err)
		}
		if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.IsDir() {
			return Result{}, errors.New("source root must be a real directory, not a symlink")
		}
		sourceRoot, err = os.OpenRoot(volume.SourceRoot)
		if err != nil {
			return Result{}, fmt.Errorf("open source root: %w", err)
		}
		defer sourceRoot.Close()
		openedSourceInfo, err := sourceRoot.Stat(".")
		if err != nil || !os.SameFile(sourceInfo, openedSourceInfo) {
			return Result{}, errors.New("source root changed while opening")
		}
	}
	finalName := fmt.Sprintf("%s Volume %02d.cbz", volume.Series, volume.Number)
	final := filepath.Join(volume.SeriesDir, finalName)
	result := Result{Path: final, Preset: preset}
	chapters, err := prepareChapters(sourceRoot, volume.Chapters, preset)
	if err != nil {
		return Result{}, fmt.Errorf("volume %02d incomplete: %w", volume.Number, err)
	}
	archive, part, err := createArchivePart(outputRoot, finalName)
	if err != nil {
		return Result{}, fmt.Errorf("create archive part for %s: %w", final, err)
	}
	defer outputRoot.Remove(part)
	if err := archive.Chmod(0o644); err != nil {
		_ = archive.Close()
		return Result{}, fmt.Errorf("set archive part permissions: %w", err)
	}
	zipper := zip.NewWriter(archive)
	for _, chapter := range chapters {
		if err := ctx.Err(); err != nil {
			_ = zipper.Close()
			_ = archive.Close()
			return Result{}, fmt.Errorf("pack interrupted: %w", err)
		}
		for _, source := range chapter.pages {
			if err := ctx.Err(); err != nil {
				_ = zipper.Close()
				_ = archive.Close()
				return Result{}, fmt.Errorf("pack interrupted: %w", err)
			}
			base := fmt.Sprintf("Chapter %03d/%03d", int(chapter.Number), source.number)
			name := base + source.ext
			if preset == Raw {
				err = addStoredImage(sourceRoot, zipper, name, source)
			} else {
				name = base + ".jpg"
				err = addConvertedImage(sourceRoot, zipper, name, source, settings)
				if err != nil && isDecodeError(err) {
					name = base + source.ext
					result.Warnings = append(result.Warnings, Warning{Entry: name, Source: source.path, Err: err})
					err = addStoredImage(sourceRoot, zipper, name, source)
				}
			}
			if err != nil {
				_ = zipper.Close()
				_ = archive.Close()
				return Result{}, fmt.Errorf("pack %s: %w", name, err)
			}
		}
	}
	if err := zipper.Close(); err != nil {
		_ = archive.Close()
		return Result{}, fmt.Errorf("close CBZ: %w", err)
	}
	if err := archive.Sync(); err != nil {
		_ = archive.Close()
		return Result{}, fmt.Errorf("sync CBZ: %w", err)
	}
	if err := archive.Close(); err != nil {
		return Result{}, fmt.Errorf("close archive file: %w", err)
	}
	if err := atomicRootRename(outputRoot, part, finalName); err != nil {
		return Result{}, fmt.Errorf("publish archive %s: %w", final, err)
	}
	if err := syncRootDirectory(outputRoot); err != nil {
		return Result{}, fmt.Errorf("sync archive directory: %w", err)
	}
	return result, nil
}
func createArchivePart(root *os.Root, finalName string) (*os.File, string, error) {
	for range 16 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := finalName + "." + hex.EncodeToString(random[:]) + ".part"
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not allocate a unique archive staging file")
}

func syncRootDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type preparedChapter struct {
	Chapter
	pages []page
}

func prepareChapters(root *os.Root, chapters []Chapter, preset Preset) ([]preparedChapter, error) {
	if len(chapters) == 0 {
		return nil, errors.New("no discovered chapters mapped to volume")
	}
	prepared := make([]preparedChapter, 0, len(chapters))
	seen := make(map[int]bool, len(chapters))
	var totalBytes int64
	for _, chapter := range chapters {
		number, err := strconv.Atoi(chapter.Display)
		if err != nil || number <= 0 || float64(number) != chapter.Number {
			return nil, fmt.Errorf("invalid integer chapter %q", chapter.Display)
		}
		if seen[number] {
			return nil, fmt.Errorf("duplicate chapter entry Chapter %03d", number)
		}
		seen[number] = true
		pages, bytes, err := chapterPages(root, chapter, preset)
		if err != nil {
			return nil, fmt.Errorf("chapter %s: %w", chapter.Display, err)
		}
		if bytes > MaxVolumeBytes-totalBytes {
			return nil, fmt.Errorf("volume source exceeds byte limit %d", MaxVolumeBytes)
		}
		totalBytes += bytes
		prepared = append(prepared, preparedChapter{Chapter: chapter, pages: pages})
	}
	sort.SliceStable(prepared, func(i, j int) bool { return prepared[i].Number < prepared[j].Number })
	return prepared, nil
}

func lstatRealRootPath(root *os.Root, relative string) (os.FileInfo, error) {
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("source path escapes root")
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

func chapterPages(root *os.Root, chapter Chapter, preset Preset) ([]page, int64, error) {
	if chapter.ExpectedPages <= 0 || chapter.ExpectedPages > store.MaxChapterPages {
		return nil, 0, fmt.Errorf("expected page count must be between 1 and %d", store.MaxChapterPages)
	}
	var (
		info os.FileInfo
		dir  *os.File
		err  error
	)
	if root != nil {
		info, err = lstatRealRootPath(root, chapter.Dir)
	} else {
		info, err = os.Lstat(chapter.Dir)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("inspect chapter source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, 0, errors.New("chapter source must be a real directory, not a symlink")
	}
	afterChapterSourceInspect(chapter.Dir)
	if root != nil {
		dir, err = root.Open(chapter.Dir)
	} else {
		dir, err = os.Open(chapter.Dir)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open chapter source: %w", err)
	}
	defer dir.Close()
	openedInfo, err := dir.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, 0, errors.New("chapter source changed while opening")
	}
	byNumber := make(map[int]page, chapter.ExpectedPages)
	var totalBytes int64
	for {
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return nil, 0, fmt.Errorf("page source %s is a symlink", entry.Name())
			}
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			ext := filepath.Ext(entry.Name())
			base := strings.TrimSuffix(entry.Name(), ext)
			number, parseErr := strconv.Atoi(base)
			if parseErr != nil || number <= 0 || ext == "" {
				continue
			}
			if number > store.MaxChapterPages || len(byNumber) >= store.MaxChapterPages {
				return nil, 0, fmt.Errorf("page number exceeds limit %d", store.MaxChapterPages)
			}
			path := filepath.Join(chapter.Dir, entry.Name())
			if previous, exists := byNumber[number]; exists {
				return nil, 0, fmt.Errorf("duplicate page %03d (%s and %s)", number, filepath.Base(previous.path), entry.Name())
			}
			format, size, fileInfo, inspectErr := inspectPage(root, path, preset)
			if inspectErr != nil {
				return nil, 0, fmt.Errorf("page %s: %w", entry.Name(), inspectErr)
			}
			if size > MaxVolumeBytes-totalBytes {
				return nil, 0, fmt.Errorf("volume source exceeds byte limit %d", MaxVolumeBytes)
			}
			totalBytes += size
			byNumber[number] = page{number: number, path: path, ext: "." + string(format), size: size, info: fileInfo}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, 0, fmt.Errorf("read downloaded pages from %s: %w", chapter.Dir, readErr)
		}
	}
	pages := make([]page, 0, len(byNumber))
	for number := 1; number <= chapter.ExpectedPages; number++ {
		imagePage, exists := byNumber[number]
		if !exists {
			return nil, 0, fmt.Errorf("missing page %03d; expected %d pages", number, chapter.ExpectedPages)
		}
		pages = append(pages, imagePage)
	}
	if len(byNumber) != chapter.ExpectedPages {
		return nil, 0, fmt.Errorf("expected %d pages, found %d numbered image files", chapter.ExpectedPages, len(byNumber))
	}
	return pages, totalBytes, nil
}

func inspectPage(root *os.Root, filename string, preset Preset) (komiku.ImageFormat, int64, os.FileInfo, error) {
	file, info, err := openRegularSource(root, filename, nil)
	if err != nil {
		return "", 0, nil, err
	}
	defer file.Close()
	if info.Size() <= 0 || info.Size() > MaxPageBytes {
		return "", 0, nil, fmt.Errorf("image size %d is outside limit 1-%d", info.Size(), MaxPageBytes)
	}
	var header [12]byte
	n, readErr := io.ReadFull(file, header[:])
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return "", 0, nil, readErr
	}
	format, valid := komiku.DetectImage(header[:n])
	if !valid {
		return "", 0, nil, fmt.Errorf("invalid image: %s is not a valid image", filename)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, nil, err
	}
	config, _, decodeErr := image.DecodeConfig(file)
	if decodeErr == nil {
		if err := validateImageDimensions(config.Width, config.Height); err != nil {
			return "", 0, nil, err
		}
	}
	return format, info.Size(), info, nil
}

func openRegularSource(root *os.Root, filename string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	var (
		before os.FileInfo
		file   *os.File
		err    error
	)
	if root != nil {
		before, err = root.Lstat(filename)
		if err == nil {
			file, err = root.Open(filename)
		}
	} else {
		before, err = os.Lstat(filename)
		if err == nil {
			file, err = os.Open(filename)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		file.Close()
		return nil, nil, fmt.Errorf("page source is not a regular non-symlink file: %s", filename)
	}
	if expected != nil && !os.SameFile(expected, before) {
		file.Close()
		return nil, nil, fmt.Errorf("page source changed before opening: %s", filename)
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !os.SameFile(before, after) || (expected != nil && !os.SameFile(expected, after)) {
		file.Close()
		return nil, nil, fmt.Errorf("page source changed while opening: %s", filename)
	}
	return file, after, nil
}

func addStoredImage(root *os.Root, archive *zip.Writer, name string, source page) error {
	file, info, err := openRegularSource(root, source.path, source.info)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() != source.size {
		return fmt.Errorf("page source size changed: %s", source.path)
	}
	var header [12]byte
	n, readErr := io.ReadFull(file, header[:])
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return readErr
	}
	format, valid := komiku.DetectImage(header[:n])
	if !valid || "."+string(format) != source.ext {
		return fmt.Errorf("page source format changed: %s", source.path)
	}
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := entry.Write(header[:n]); err != nil {
		return err
	}
	copied, err := io.Copy(entry, io.LimitReader(file, source.size-int64(n)+1))
	if err != nil {
		return err
	}
	if copied != source.size-int64(n) {
		return fmt.Errorf("page source size changed while reading: %s", source.path)
	}
	return nil
}

type decodeError struct{ err error }

func (e decodeError) Error() string { return e.err.Error() }
func (e decodeError) Unwrap() error { return e.err }

func isDecodeError(err error) bool {
	var target decodeError
	return errors.As(err, &target)
}

func addConvertedImage(root *os.Root, archive *zip.Writer, name string, source page, settings presetSettings) error {
	file, info, err := openRegularSource(root, source.path, source.info)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() != source.size {
		return fmt.Errorf("page source size changed: %s", source.path)
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return decodeError{err: err}
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if err := validateImageDimensions(width, height); err != nil {
		return err
	}
	longest := max(width, height)
	output := decoded
	if longest > settings.longestSide {
		newWidth := max(1, width*settings.longestSide/longest)
		newHeight := max(1, height*settings.longestSide/longest)
		resized := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		draw.CatmullRom.Scale(resized, resized.Bounds(), decoded, bounds, draw.Over, nil)
		output = resized
	}
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return err
	}
	return jpeg.Encode(entry, output, &jpeg.Options{Quality: settings.quality})
}

func validateImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 || width > MaxImageDimension || height > MaxImageDimension || int64(width) > MaxImagePixels/int64(height) {
		return fmt.Errorf("image dimensions %dx%d exceed limits", width, height)
	}
	return nil
}

func validateArchiveLeaf(name string) error {
	if name == "" || name == "." || name == ".." || name != strings.TrimSpace(name) || len(name) > 200 || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid series name %q", name)
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("invalid series name %q", name)
		}
	}
	return nil
}
