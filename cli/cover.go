package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bangadam/komiku-cli/komiku"
)

const coverCommandUsage = "usage: komiku-cli cover <series-url>|--all [--out DIR] [--force] [--json]"

// CoverReport is one cover download outcome.
type CoverReport struct {
	Slug    string `json:"slug"`
	Cover   string `json:"cover,omitempty"`
	Source  string `json:"source,omitempty"`
	Bytes   int    `json:"bytes,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

func NewCoverCommand(dependencies Dependencies) *cobra.Command {
	var all, asJSON, force bool
	cmd := &cobra.Command{
		Use:   "cover [series-url]",
		Short: "Save series cover art into the local library",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCover(cmd.Context(), args, all, asJSON, force, cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "fetch covers for every series in the library")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing cover")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	cmd.Flags().String("out", "", "library root")
	return cmd
}

func runCover(ctx context.Context, args []string, all, asJSON, force bool, flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) error {
	if all == (len(args) == 1) {
		return errors.New(coverCommandUsage)
	}
	output, _ := flags.GetString("out")
	overrides := Overrides{}
	if flags.Changed("out") {
		overrides.OutputRoot = &output
	}
	config, err := loadEffectiveConfig(dependencies, overrides)
	if err != nil {
		return err
	}
	client := dependencies.newKomikuClient(config.ImageDelay)

	var targets []localSeriesTarget
	if all {
		targets, err = localSeriesTargets(config.OutputRoot, dependencies)
		if err != nil {
			return err
		}
	} else {
		parsed, err := komiku.ValidateSeriesURL(args[0])
		if err != nil {
			return err
		}
		slug := SeriesSlug(parsed)
		if slug == "" {
			return fmt.Errorf("cannot derive series directory from URL %q", args[0])
		}
		targets = []localSeriesTarget{{Slug: slug, URL: args[0]}}
	}

	reports := make([]CoverReport, 0, len(targets))
	failed := 0
	for _, target := range targets {
		report := saveSeriesCover(ctx, client, config.OutputRoot, target, force)
		if report.Error != "" {
			failed++
		}
		reports = append(reports, report)
	}

	if asJSON {
		if err := json.NewEncoder(stdout).Encode(reports); err != nil {
			return err
		}
	} else {
		for _, report := range reports {
			switch {
			case report.Error != "":
				fmt.Fprintf(stdout, "%s: error: %s\n", report.Slug, report.Error)
			case report.Skipped:
				fmt.Fprintf(stdout, "%s: cover already saved: %s\n", report.Slug, report.Cover)
			default:
				fmt.Fprintf(stdout, "%s: saved %s (%d bytes)\n", report.Slug, report.Cover, report.Bytes)
			}
		}
	}

	// A single-series request must fail loudly; a library backfill reports
	// per-series problems but still succeeds when at least one cover landed.
	if !all && failed > 0 {
		return errors.New("cover download failed")
	}
	if all && failed == len(targets) && len(targets) > 0 {
		return errors.New("cover download failed for every series")
	}
	return nil
}

func saveSeriesCover(ctx context.Context, client *komiku.Client, root string, target localSeriesTarget, force bool) CoverReport {
	report := CoverReport{Slug: target.Slug}
	dir := filepath.Join(root, target.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		report.Error = err.Error()
		return report
	}
	if existing, ok := existingCover(dir); ok && !force {
		report.Cover = existing
		report.Skipped = true
		return report
	}
	coverURL, err := client.CoverURL(ctx, target.URL)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Source = coverURL
	data, format, err := client.DownloadCover(ctx, coverURL, target.URL)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	path := filepath.Join(dir, "cover."+string(format))
	if err := writeFileAtomic(path, data); err != nil {
		report.Error = err.Error()
		return report
	}
	removeOtherCovers(dir, path)
	report.Cover = path
	report.Bytes = len(data)
	return report
}

// existingCover returns the first cover file present in a series directory.
func existingCover(dir string) (string, bool) {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		path := filepath.Join(dir, "cover"+ext)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return path, true
		}
	}
	return "", false
}

// removeOtherCovers drops stale covers left by an earlier format so a series
// never shows two competing cover files.
func removeOtherCovers(dir, keep string) {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		path := filepath.Join(dir, "cover"+ext)
		if path == keep {
			continue
		}
		_ = os.Remove(path)
	}
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cover-*")
	if err != nil {
		return fmt.Errorf("create temp cover: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write cover: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync cover: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close cover: %w", err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("chmod cover: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("save cover: %w", err)
	}
	return nil
}

// fetchCoverBestEffort saves a cover after a download without turning a cover
// problem into a download failure.
func fetchCoverBestEffort(ctx context.Context, client *komiku.Client, seriesDir, seriesURL string) (string, error) {
	if _, ok := existingCover(seriesDir); ok {
		return "", nil
	}
	target := localSeriesTarget{Slug: filepath.Base(seriesDir), URL: seriesURL}
	report := saveSeriesCover(ctx, client, filepath.Dir(seriesDir), target, false)
	if report.Error != "" {
		return "", errors.New(report.Error)
	}
	return report.Cover, nil
}
