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

// UpdateReport is the structured result of an update run across the library.
type UpdateReport struct {
	Series []UpdateSeries `json:"series"`
}

type UpdateSeries struct {
	Slug        string   `json:"slug"`
	SeriesURL   string   `json:"series_url"`
	NewChapters []string `json:"new_chapters,omitempty"`
	Downloaded  int      `json:"downloaded"`
	Skipped     int      `json:"skipped"`
	Failed      int      `json:"failed"`
	Error       string   `json:"error,omitempty"`
}

func NewUpdateCommand(dependencies Dependencies) *cobra.Command {
	var (
		asJSON bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download new chapters across all downloaded series in the local library",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context(), dryRun, asJSON, cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "check", false, "dry run: report new chapters without downloading")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the update report as JSON")
	cmd.Flags().String("out", "", "local library root to update")
	cmd.Flags().String("delay", "", "image request delay")
	cmd.Flags().Int("workers", 3, "chapter worker count")
	return cmd
}

func runUpdate(ctx context.Context, dryRun, asJSON bool, flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) error {
	overrides := Overrides{}
	if flags.Changed("out") {
		out, _ := flags.GetString("out")
		overrides.OutputRoot = &out
	}
	if flags.Changed("delay") {
		delayText, _ := flags.GetString("delay")
		parsed, err := time.ParseDuration(delayText)
		if err != nil {
			return fmt.Errorf("invalid --delay: %w", err)
		}
		overrides.ImageDelay = &parsed
	}
	workers, _ := flags.GetInt("workers")
	if workers <= 0 || workers > 32 {
		return errors.New("workers must be between 1 and 32")
	}
	config, err := loadEffectiveConfig(dependencies, overrides)
	if err != nil {
		return err
	}
	targets, err := localSeriesTargets(config.OutputRoot, dependencies)
	if err != nil {
		return err
	}
	report := UpdateReport{}
	for _, target := range targets {
		if !seriesHasDownloads(filepath.Join(config.OutputRoot, target.Slug)) {
			continue
		}
		entry := UpdateSeries{Slug: target.Slug, SeriesURL: target.URL}
		if err := ctx.Err(); err != nil {
			entry.Error = "interrupted"
			report.Series = append(report.Series, entry)
			break
		}
		result, err := updateOneSeries(ctx, target, config, dependencies, dryRun, workers)
		entry.NewChapters = result.NewChapters
		entry.Downloaded = result.Downloaded
		entry.Skipped = result.Skipped
		entry.Failed = result.Failed
		if err != nil {
			entry.Error = err.Error()
		}
		report.Series = append(report.Series, entry)
	}

	if asJSON {
		return json.NewEncoder(stdout).Encode(report)
	}
	if len(report.Series) == 0 {
		fmt.Fprintln(stdout, "no series downloaded yet")
		return nil
	}
	for _, s := range report.Series {
		if s.Error != "" {
			fmt.Fprintf(stdout, "%s  ERROR  %s\n", s.Slug, s.Error)
			continue
		}
		newCount := len(s.NewChapters)
		fmt.Fprintf(stdout, "%s  new=%d downloaded=%d skipped=%d failed=%d\n", s.Slug, newCount, s.Downloaded, s.Skipped, s.Failed)
		if newCount > 0 && newCount <= 20 {
			fmt.Fprintf(stdout, "  new chapters: %s\n", strings.Join(s.NewChapters, ", "))
		}
	}
	return nil
}

type updateResult struct {
	NewChapters []string
	Downloaded  int
	Skipped     int
	Failed      int
}

// updateOneSeries discovers a series's chapters, identifies those not yet
// marked done locally, and (unless dryRun) downloads them via the engine.
func updateOneSeries(ctx context.Context, target localSeriesTarget, config Config, dependencies Dependencies, dryRun bool, workers int) (updateResult, error) {
	if _, err := komiku.ValidateSeriesURL(target.URL); err != nil {
		return updateResult{}, err
	}
	// Read completion state without opening a writable store: checks must not
	// create directories, state files, or audit logs.
	doneNumbers, err := store.ReadDone(config.OutputRoot, target.Slug)
	if err != nil {
		return updateResult{}, err
	}
	done := make(map[float64]bool, len(doneNumbers))
	for _, number := range doneNumbers {
		done[number] = true
	}
	client := dependencies.newKomikuClient(config.ImageDelay)
	chapters, err := client.Discover(ctx, target.URL)
	if err != nil {
		return updateResult{}, fmt.Errorf("discover: %w", err)
	}
	if len(chapters) == 0 {
		return updateResult{}, errors.New("series discovery returned no chapter hrefs")
	}
	ambiguous := ambiguousChapterNumbers(chapters)
	var jobs []Job
	var newChapters []string
	for _, chapter := range chapters {
		if done[chapter.Number] {
			continue
		}
		newChapters = append(newChapters, chapter.Display)
		jobs = append(jobs, Job{Chapter: chapter, Flat: true, Ambiguous: ambiguous[chapter.Number]})
	}
	result := updateResult{NewChapters: newChapters, Skipped: len(chapters) - len(jobs)}
	if dryRun || len(jobs) == 0 {
		return result, nil
	}
	seriesStore, err := store.Open(config.OutputRoot, target.Slug)
	if err != nil {
		return result, err
	}
	if workers <= 0 {
		workers = 3
	}
	engine := Engine{Client: client, Store: seriesStore, Workers: workers, Now: dependencies.Now}
	summary := engine.Run(ctx, jobs)
	result.Downloaded = summary.Counts[Done]
	result.Failed = summary.Counts[Part] + summary.Counts[Fail] + summary.Counts[NoImg]
	if summary.Err != nil {
		return result, summary.Err
	}
	return result, nil
}
