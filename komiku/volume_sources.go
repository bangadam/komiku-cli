// volume_sources.go selects and orders the providers that supply chapter-to-volume mappings.
package komiku

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// VolumeSource selects which provider supplies the chapter→volume mapping.
type VolumeSource string

const (
	// VolumeSourceAuto tries MangaDex first, then Wikipedia, then Fandom.
	VolumeSourceAuto VolumeSource = "auto"
	// VolumeSourceMangaDex uses MangaDex only.
	VolumeSourceMangaDex VolumeSource = "mangadex"
	// VolumeSourceWikipedia uses Wikipedia only.
	VolumeSourceWikipedia VolumeSource = "wikipedia"
	// VolumeSourceFandom uses the community Fandom wiki only.
	VolumeSourceFandom VolumeSource = "fandom"
)

// ParseVolumeSource validates a --volume-source value.
func ParseVolumeSource(value string) (VolumeSource, error) {
	switch VolumeSource(strings.ToLower(strings.TrimSpace(value))) {
	case "", VolumeSourceAuto:
		return VolumeSourceAuto, nil
	case VolumeSourceMangaDex:
		return VolumeSourceMangaDex, nil
	case VolumeSourceWikipedia:
		return VolumeSourceWikipedia, nil
	case VolumeSourceFandom:
		return VolumeSourceFandom, nil
	default:
		return "", fmt.Errorf("unknown volume source %q; expected auto, mangadex, wikipedia, or fandom", value)
	}
}

// minVolumeCoverage is the fraction of the discovered chapter range a mapping
// must reach to be accepted in auto mode. MangaDex in particular often stops
// several volumes short, and a partial mapping silently drops later volumes.
const minVolumeCoverage = 0.9

// volumeSource is one candidate provider of a chapter→volume mapping.
type volumeSource struct {
	name  string
	fetch func(context.Context) ([]Volume, string, error)
}

// FetchVolumeMapping resolves a chapter→volume mapping from MangaDex, the
// default provider. Use FetchVolumeMappingFrom with VolumeSourceAuto to try
// Wikipedia and Fandom as fallbacks.
func (c *Client) FetchVolumeMapping(ctx context.Context, seriesName string, maxChapter int) (VolumeCache, error) {
	return c.FetchVolumeMappingFrom(ctx, seriesName, maxChapter, VolumeSourceMangaDex)
}

// FetchVolumeMappingFrom resolves a chapter→volume mapping from the requested
// provider. In auto mode a source whose mapping stops well short of the last
// discovered chapter is rejected and the next provider is tried.
func (c *Client) FetchVolumeMappingFrom(ctx context.Context, seriesName string, maxChapter int, source VolumeSource) (VolumeCache, error) {
	name := strings.TrimSpace(seriesName)
	if name == "" {
		return VolumeCache{}, errors.New("series name is empty")
	}

	attempts := make([]string, 0, 6)
	remember := func(label string, err error) {
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %v", label, err))
		}
	}

	for _, candidate := range c.volumeSources(name, maxChapter, source, remember) {
		if err := ctx.Err(); err != nil {
			return VolumeCache{}, err
		}
		volumes, url, err := candidate.fetch(ctx)
		if err != nil {
			remember(candidate.name, err)
			continue
		}
		if len(volumes) == 0 {
			remember(candidate.name, errors.New("no volume grouping"))
			continue
		}
		if err := ValidateVolumes(volumes, maxChapter); err != nil {
			remember(candidate.name, err)
			continue
		}
		if source == VolumeSourceAuto && !coversChapterRange(volumes, maxChapter) {
			remember(candidate.name, fmt.Errorf("incomplete mapping: reaches chapter %d of %d", highestMappedChapter(volumes), maxChapter))
			continue
		}
		return VolumeCache{Source: url, Volumes: volumes}, nil
	}

	if len(attempts) == 0 {
		return VolumeCache{}, errors.New("automatic volume mapping failed: no source available")
	}
	return VolumeCache{}, fmt.Errorf(
		"automatic volume mapping failed (tried %d sources): %s\ncreate/edit .volumes.json or use --ch ... --flat",
		len(attempts),
		strings.Join(attempts, "; "),
	)
}

// coversChapterRange reports whether a mapping reaches far enough into the
// discovered chapter range to be trusted in auto mode.
func coversChapterRange(volumes []Volume, maxChapter int) bool {
	if maxChapter <= 0 {
		return true
	}
	return float64(highestMappedChapter(volumes)) >= float64(maxChapter)*minVolumeCoverage
}

func highestMappedChapter(volumes []Volume) int {
	highest := 0
	for _, volume := range volumes {
		if volume.End > highest {
			highest = volume.End
		}
	}
	return highest
}

// volumeSources builds the ordered provider list for a series name and the
// requested source selection.
func (c *Client) volumeSources(name string, maxChapter int, source VolumeSource, remember func(string, error)) []volumeSource {
	mangadex := volumeSource{
		name: "MangaDex",
		fetch: func(ctx context.Context) ([]Volume, string, error) {
			return c.fetchMangaDexVolumes(ctx, name, maxChapter)
		},
	}
	wikipedia := c.wikipediaVolumeSources(name, maxChapter, remember)
	fandom := volumeSource{
		name: "Fandom",
		fetch: func(ctx context.Context) ([]Volume, string, error) {
			return c.fetchFandomVolumes(ctx, name, maxChapter)
		},
	}

	switch source {
	case VolumeSourceMangaDex:
		return []volumeSource{mangadex}
	case VolumeSourceWikipedia:
		return wikipedia
	case VolumeSourceFandom:
		return []volumeSource{fandom}
	default:
		ordered := make([]volumeSource, 0, len(wikipedia)+2)
		ordered = append(ordered, mangadex)
		ordered = append(ordered, wikipedia...)
		ordered = append(ordered, fandom)
		return ordered
	}
}

// wikipediaVolumeSources builds the Wikipedia provider chain: list pages, the
// main article, then a search fallback for noisy slugs.
func (c *Client) wikipediaVolumeSources(name string, maxChapter int, remember func(string, error)) []volumeSource {
	sources := make([]volumeSource, 0, 3)
	for _, title := range []string{"List of " + name + " chapters", "List of " + name + " volumes", name} {
		title := title
		sources = append(sources, volumeSource{
			name: "Wikipedia " + title,
			fetch: func(ctx context.Context) ([]Volume, string, error) {
				return c.fetchWikipediaVolumes(ctx, title, maxChapter)
			},
		})
	}
	sources = append(sources, volumeSource{
		name: "Wikipedia search",
		fetch: func(ctx context.Context) ([]Volume, string, error) {
			return c.fetchWikipediaVolumesBySearch(ctx, name, maxChapter, remember)
		},
	})
	return sources
}
