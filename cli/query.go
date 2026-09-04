package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Series    string        `json:"series"`
	SeriesURL string        `json:"series_url,omitempty"`
	Chapters  []ChapterInfo `json:"chapters"`
	DoneCount int           `json:"done_count"`
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
	client := dependencies.newKomikuClient(0)
	chapters, err := client.Discover(ctx, seriesURL)
	if err != nil {
		return fmt.Errorf("discover chapters: %w", err)
	}
	if len(chapters) == 0 {
		return errors.New("series discovery returned no chapter hrefs")
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
	done, err := store.ReadDone(config.OutputRoot, series)
	if err != nil {
		return err
	}
	doneSet := make(map[float64]bool, len(done))
	for _, chapter := range done {
		doneSet[chapter] = true
	}
	info := SeriesInfo{Series: series, SeriesURL: seriesURL, Chapters: make([]ChapterInfo, 0, len(chapters))}
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
	if asJSON {
		return json.NewEncoder(stdout).Encode(info)
	}
	fmt.Fprintf(stdout, "%s  chapters=%d done=%d\n", info.Series, len(info.Chapters), info.DoneCount)
	for _, chapter := range info.Chapters {
		marker := "[ ]"
		if chapter.Done {
			marker = "[x]"
		}
		fmt.Fprintf(stdout, "%s %s  %s\n", marker, chapter.Display, chapter.URL)
	}
	return nil
}
