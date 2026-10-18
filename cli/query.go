package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

// loadEffectiveConfig resolves the saved config plus explicit overrides for
// read-only commands that need the output root.
func loadEffectiveConfig(dependencies Dependencies, overrides Overrides) (Config, error) {
	configPath := dependencies.ConfigPath
	if configPath == "" {
		var err error
		configPath, err = ConfigPath()
		if err != nil {
			return Config{}, err
		}
	}
	fileConfig, err := LoadFileConfig(configPath)
	if err != nil {
		return Config{}, err
	}
	return ResolveConfig(fileConfig, overrides)
}

func (d Dependencies) newKomikuClient(imageDelay time.Duration) *komiku.Client {
	client := komiku.NewClient(d.HTTP, imageDelay)
	if d.BaseURL != "" {
		client.BaseURL = d.BaseURL
	}
	return client
}

func NewSearchCommand(dependencies Dependencies) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search Komiku titles and print series URLs",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
				return errors.New("search requires exactly one non-empty query")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSearch(cmd.Context(), args[0], asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}

func runSearch(ctx context.Context, query string, asJSON bool, stdout io.Writer, dependencies Dependencies) error {
	client := dependencies.newKomikuClient(0)
	results, err := client.Search(ctx, query)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(results)
	}
	for _, series := range results {
		fmt.Fprintf(stdout, "%s\t%s\n", series.Title, series.URL)
	}
	return nil
}

// ChapterInfo is one discovered chapter with local download status.
type ChapterInfo struct {
	Display string  `json:"display"`
	Number  float64 `json:"number"`
	URL     string  `json:"url"`
	Done    bool    `json:"done"`
}

// SeriesInfo is the headless series report printed by info.
type SeriesInfo struct {
	Series    string          `json:"series"`
	SeriesURL string          `json:"series_url,omitempty"`
	Chapters  []ChapterInfo   `json:"chapters"`
	DoneCount int             `json:"done_count"`
	Volumes   []komiku.Volume `json:"volumes,omitempty"`
	// VolumeError reports why the optional volume mapping is unavailable. The
	// chapter list is still complete, so callers can fall back to a flat pack.
	VolumeError string `json:"volume_error,omitempty"`
	// Cover is the local cover file for this series, when one was saved.
	Cover string `json:"cover,omitempty"`
}

func NewInfoCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info <series-url>",
		Short: "List a series's chapters with local download status",
		Args:  exactOneArg("usage: komiku-cli info <series-url> [--out DIR] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInfo(cmd.Context(), args[0], cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().String("out", "", "download root to inspect")
	cmd.Flags().Bool("json", false, "print series info as JSON")
	cmd.Flags().Bool("volumes", false, "include the volume mapping (cache first, then an online source)")
	cmd.Flags().Bool("offline", false, "read chapters and volumes from disk only; never touch the network")
	cmd.Flags().String("volume-source", "mangadex", "volume mapping source: mangadex (default), auto, wikipedia, or fandom")
	return cmd
}

func runInfo(ctx context.Context, seriesURL string, flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) error {
	parsed, err := komiku.ValidateSeriesURL(seriesURL)
	if err != nil {
		return err
	}
	series := SeriesSlug(parsed)
	if series == "" {
		return fmt.Errorf("cannot derive series directory from URL %q", seriesURL)
	}
	output, _ := flags.GetString("out")
	asJSON, _ := flags.GetBool("json")
	overrides := Overrides{}
	if flags.Changed("out") {
		overrides.OutputRoot = &output
	}
	config, err := loadEffectiveConfig(dependencies, overrides)
	if err != nil {
		return err
	}
	offline, _ := flags.GetBool("offline")
	includeVolumes, _ := flags.GetBool("volumes")

	var info SeriesInfo
	if offline {
		info, err = localSeriesInfo(config.OutputRoot, series, seriesURL, includeVolumes)
		if err != nil {
			return err
		}
	} else {
		client := dependencies.newKomikuClient(0)
		chapters, discoverErr := client.Discover(ctx, seriesURL)
		if discoverErr != nil {
			return fmt.Errorf("discover chapters: %w", discoverErr)
		}
		if len(chapters) == 0 {
			return errors.New("series discovery returned no chapter hrefs")
		}
		done, doneErr := store.ReadDone(config.OutputRoot, series)
		if doneErr != nil {
			return doneErr
		}
		doneSet := make(map[float64]bool, len(done))
		for _, chapter := range done {
			doneSet[chapter] = true
		}
		info = SeriesInfo{Series: series, SeriesURL: seriesURL, Chapters: make([]ChapterInfo, 0, len(chapters))}
		if cover, ok := existingCover(filepath.Join(config.OutputRoot, series)); ok {
			info.Cover = cover
		}
		for _, chapter := range chapters {
			info.Chapters = append(info.Chapters, ChapterInfo{
				Display: chapter.Display,
				Number:  chapter.Number,
				URL:     chapter.URL,
				Done:    doneSet[chapter.Number],
			})
			if doneSet[chapter.Number] {
				info.DoneCount++
			}
		}
		volumeSourceName, _ := flags.GetString("volume-source")
		if includeVolumes {
			seriesStore, storeErr := store.Open(config.OutputRoot, series)
			if storeErr != nil {
				return storeErr
			}
			volumeSource, sourceErr := komiku.ParseVolumeSource(volumeSourceName)
			if sourceErr != nil {
				return sourceErr
			}
			mapping, mapErr := LoadVolumeMappingFrom(ctx, client, seriesStore.Dir(), series, chapters, volumeSource, flags.Changed("volume-source"))
			if mapErr != nil {
				// The chapter list is still useful without a mapping, and the pack
				// command can fall back to a flat run, so keep the report.
				info.VolumeError = mapErr.Error()
			} else {
				info.Volumes = mapping
			}
		}
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(info)
	}
	fmt.Fprintf(stdout, "%s  chapters=%d done=%d\n", info.Series, len(info.Chapters), info.DoneCount)
	if info.VolumeError != "" {
		fmt.Fprintf(stdout, "warning: volume mapping unavailable: %s\n", info.VolumeError)
	}
	for _, chapter := range info.Chapters {
		marker := "[ ]"
		if chapter.Done {
			marker = "[x]"
		}
		fmt.Fprintf(stdout, "%s %s  %s\n", marker, chapter.Display, chapter.URL)
	}
	return nil
}
