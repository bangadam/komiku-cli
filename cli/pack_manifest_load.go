// pack_manifest_load.go reads, validates, and decodes existing .pack.json manifests.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func LoadPackManifest(seriesDir string) (PackManifest, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return PackManifest{}, err
	}
	manifest, err := loadPackManifestIfPresent(root)
	if err != nil {
		return PackManifest{}, err
	}
	if manifest.Version == 0 {
		return PackManifest{}, fmt.Errorf("%w at %s", errPackManifestNotFound, PackManifestPath(root))
	}
	if err := validatePackManifest(manifest); err != nil {
		return PackManifest{}, err
	}
	return manifest, nil
}

func loadPackManifestIfPresent(seriesDir string) (PackManifest, error) {
	confined, err := os.OpenRoot(seriesDir)
	if err != nil {
		return PackManifest{}, fmt.Errorf("open pack manifest root: %w", err)
	}
	defer confined.Close()
	info, err := confined.Lstat(".pack.json")
	if errors.Is(err, os.ErrNotExist) {
		return PackManifest{}, nil
	}
	if err != nil {
		return PackManifest{}, fmt.Errorf("inspect pack manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return PackManifest{}, errors.New("pack manifest must be a regular non-symlink file")
	}
	if info.Size() > maxPackManifestBytes {
		return PackManifest{}, fmt.Errorf("pack manifest exceeds byte limit %d", maxPackManifestBytes)
	}
	file, err := confined.Open(".pack.json")
	if err != nil {
		return PackManifest{}, fmt.Errorf("read pack manifest: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return PackManifest{}, errors.New("pack manifest changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPackManifestBytes+1))
	if err != nil {
		return PackManifest{}, fmt.Errorf("read pack manifest: %w", err)
	}
	if int64(len(data)) > maxPackManifestBytes {
		return PackManifest{}, fmt.Errorf("pack manifest exceeds byte limit %d", maxPackManifestBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return PackManifest{}, fmt.Errorf("invalid pack manifest JSON: %w", err)
	}
	var manifest PackManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return PackManifest{}, fmt.Errorf("invalid pack manifest JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return PackManifest{}, fmt.Errorf("invalid trailing pack manifest JSON: %w", err)
	}
	if manifest.Version != 0 {
		if err := validatePackManifest(manifest); err != nil {
			return PackManifest{}, err
		}
	}
	return manifest, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = true
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validatePackManifest(manifest PackManifest) error {
	if manifest.Version != PackManifestVersion {
		return fmt.Errorf("unsupported pack manifest version %d", manifest.Version)
	}
	if err := validateManifestString("series", manifest.Series, 200, true); err != nil {
		return err
	}
	if manifest.SeriesURL != "" {
		if len(manifest.SeriesURL) > 2048 {
			return errors.New("pack manifest series URL is too long")
		}
		if _, err := komiku.ValidateSeriesURL(manifest.SeriesURL); err != nil {
			return fmt.Errorf("pack manifest series URL: %w", err)
		}
	}
	if len(manifest.Mappings) == 0 || len(manifest.Mappings) > maxPackManifestMappings {
		return fmt.Errorf("pack manifest mapping count must be between 1 and %d", maxPackManifestMappings)
	}
	if len(manifest.Chapters) > maxPackManifestChapters {
		return fmt.Errorf("pack manifest chapter count exceeds %d", maxPackManifestChapters)
	}
	rows := append([]PackManifestMapping(nil), manifest.Mappings...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Start < rows[j].Start })
	mappingByVolume := make(map[int]PackManifestMapping, len(rows))
	totalCoverage := 0
	for index, mapping := range rows {
		if mapping.Volume <= 0 || mapping.Volume > maxSelectedVolume || mapping.Start <= 0 || mapping.End < mapping.Start || mapping.End > maxPackChapterNumber || mapping.End-mapping.Start+1 > store.MaxChapterPages {
			return fmt.Errorf("pack manifest volume %02d has an invalid bounded integer range", mapping.Volume)
		}
		width := mapping.End - mapping.Start + 1
		if width > maxPackManifestChapters-totalCoverage {
			return fmt.Errorf("pack manifest mappings cover more than %d chapters", maxPackManifestChapters)
		}
		totalCoverage += width
		if mapping.Provenance != "wikipedia-display" && mapping.Provenance != "wikipedia-recovery" && mapping.Provenance != "manual-range" && mapping.Provenance != "download-mapping" {
			return fmt.Errorf("pack manifest volume %02d has invalid provenance %q", mapping.Volume, mapping.Provenance)
		}
		if _, duplicate := mappingByVolume[mapping.Volume]; duplicate {
			return fmt.Errorf("pack manifest volume %02d is duplicated", mapping.Volume)
		}
		mappingByVolume[mapping.Volume] = mapping
		if index > 0 && mapping.Start <= rows[index-1].End {
			return fmt.Errorf("pack manifest volumes %02d and %02d overlap", rows[index-1].Volume, mapping.Volume)
		}
	}
	seenNumbers := make(map[float64]bool, len(manifest.Chapters))
	seenSources := make(map[string]bool, len(manifest.Chapters))
	for _, chapter := range manifest.Chapters {
		if !chapter.Completed {
			return fmt.Errorf("pack manifest chapter %q is not completed", chapter.Display)
		}
		// Chapter numbers are positive and may be fractional (1.1, 2.5) for
		// split releases; the display label must still parse to the same value.
		number, err := strconv.ParseFloat(chapter.Display, 64)
		if err != nil || number <= 0 || number != chapter.Number {
			return fmt.Errorf("pack manifest chapter %q has an inconsistent number", chapter.Display)
		}
		if seenNumbers[number] {
			return fmt.Errorf("pack manifest chapter %q is duplicated", chapter.Display)
		}
		seenNumbers[number] = true
		whole := int(number)
		mapping, exists := mappingByVolume[chapter.Volume]
		if !exists || whole < mapping.Start || whole > mapping.End {
			return fmt.Errorf("pack manifest chapter %q does not belong to declared volume %02d", chapter.Display, chapter.Volume)
		}
		if chapter.ExpectedPages <= 0 || chapter.ExpectedPages > store.MaxChapterPages {
			return fmt.Errorf("pack manifest chapter %q has invalid expected page count", chapter.Display)
		}
		if err := validateManifestSourcePath(chapter.SourceDir); err != nil {
			return fmt.Errorf("pack manifest chapter %q source: %w", chapter.Display, err)
		}
		if seenSources[chapter.SourceDir] {
			return fmt.Errorf("pack manifest source %s is claimed by multiple chapters", chapter.SourceDir)
		}
		seenSources[chapter.SourceDir] = true
		switch chapter.IdentityProvenance {
		case "komiku-download":
			if err := validateManifestString("chapter URL", chapter.URL, 2048, false); err != nil {
				return err
			}
			if err := validateManifestString("chapter raw ID", chapter.RawID, 512, false); err != nil {
				return err
			}
		case "recovered-local":
			if chapter.URL != "" || chapter.RawID != "" {
				return fmt.Errorf("recovered local chapter %q must not claim Komiku identity", chapter.Display)
			}
		default:
			return fmt.Errorf("pack manifest chapter %q has invalid identity provenance %q", chapter.Display, chapter.IdentityProvenance)
		}
	}
	return nil
}

func validateManifestString(label, value string, maximum int, safeLeaf bool) error {
	if value == "" || len(value) > maximum || value != strings.TrimSpace(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("pack manifest has invalid %s %q", label, value)
	}
	if safeLeaf && strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("pack manifest has invalid %s %q", label, value)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("pack manifest has invalid %s %q", label, value)
		}
	}
	return nil
}

func validateManifestSourcePath(source string) error {
	if source == "" || len(source) > 1024 || strings.ContainsAny(source, "\x00\\") || filepath.IsAbs(source) {
		return errors.New("source path must be a bounded relative path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(source)))
	if clean != source || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("source path is not canonical or escapes series root")
	}
	return nil
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open manifest directory for sync: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync manifest directory: %w", err)
	}
	return nil
}

func manifestChapterKey(url, rawID string, number float64) string {
	if url != "" {
		return "url:" + url
	}
	return fmt.Sprintf("local:%s:%g", rawID, number)
}
