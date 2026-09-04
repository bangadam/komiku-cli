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

// subscribeUsage is the help text for the subscribe command group.
const subscribeUsage = "usage: komiku-cli subscribe <series-url> | unsubscribe <slug-or-url> | subs"

func NewSubscribeCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subscribe <series-url>",
		Short: "Track a series for new-chapter updates",
		Args:  exactOneArg("usage: komiku-cli subscribe <series-url>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscribe(cmd.Context(), args[0], cmd.OutOrStdout(), dependencies)
		},
	}
	return cmd
}

func runSubscribe(ctx context.Context, seriesURL string, stdout io.Writer, dependencies Dependencies) error {
	parsed, err := komiku.ValidateSeriesURL(seriesURL)
	if err != nil {
		return err
	}
	slug := SeriesSlug(parsed)
	if slug == "" {
		return fmt.Errorf("cannot derive series directory from URL %q", seriesURL)
	}
	now := dependencies.now()
	path := dependencies.subsPath()
	file, err := LoadSubscriptions(path)
	if err != nil {
		return err
	}
	updated, added := AddSubscription(file, seriesURL, slug, now)
	if !added {
		fmt.Fprintf(stdout, "already subscribed: %s\n", slug)
		return nil
	}
	if err := SaveSubscriptions(path, updated); err != nil {
		return fmt.Errorf("save subscriptions: %w", err)
	}
	fmt.Fprintf(stdout, "subscribed: %s  %s\n", slug, seriesURL)
	return nil
}

func NewUnsubscribeCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unsubscribe <slug-or-url>",
		Short: "Stop tracking a series for updates",
		Args:  exactOneArg("usage: komiku-cli unsubscribe <slug-or-url>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnsubscribe(args[0], cmd.OutOrStdout(), dependencies)
		},
	}
	return cmd
}

func runUnsubscribe(identifier string, stdout io.Writer, dependencies Dependencies) error {
	path := dependencies.subsPath()
	file, err := LoadSubscriptions(path)
	if err != nil {
		return err
	}
	updated, removed := RemoveSubscription(file, identifier)
	if !removed {
		return fmt.Errorf("no subscription matching %q", identifier)
	}
	if err := SaveSubscriptions(path, updated); err != nil {
		return fmt.Errorf("save subscriptions: %w", err)
	}
	fmt.Fprintf(stdout, "unsubscribed: %s\n", identifier)
	return nil
}

func NewSubsCommand(dependencies Dependencies) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "subs",
		Short: "List subscribed series",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubs(asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print subscriptions as JSON")
	return cmd
}

func runSubs(asJSON bool, stdout io.Writer, dependencies Dependencies) error {
	path := dependencies.subsPath()
	file, err := LoadSubscriptions(path)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(file)
	}
	if len(file.Subscriptions) == 0 {
		fmt.Fprintln(stdout, "no subscriptions")
		return nil
	}
	for _, sub := range file.Subscriptions {
		last := "never"
		if !sub.LastCheck.IsZero() {
			last = humanTime(sub.LastCheck, dependencies.now())
		}
		fmt.Fprintf(stdout, "%s\t%s\tchecked %s\n", sub.Slug, sub.SeriesURL, last)
	}
	return nil
}

func humanTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return "just now"
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}

// UpdateReport is the structured result of an update run across the library.
type UpdateReport struct {
	Series []UpdateSeries `json:"series"`
}

type UpdateSeries struct {
	Slug       string   `json:"slug"`
	SeriesURL  string   `json:"series_url"`
	NewChapters []string `json:"new_chapters,omitempty"`
	Downloaded  int      `json:"downloaded"`
	Skipped     int      `json:"skipped"`
	Failed      int      `json:"failed"`
	Error      string   `json:"error,omitempty"`
}

func NewUpdateCommand(dependencies Dependencies) *cobra.Command {
	var (
		asJSON bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download new chapters across all subscribed series",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context(), dryRun, asJSON, cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "check", false, "dry run: report new chapters without downloading")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the update report as JSON")
	cmd.Flags().String("out", "", "output directory for downloads")
	cmd.Flags().String("delay", "", "image request delay")
	cmd.Flags().Int("workers", 3, "chapter worker count")
	return cmd
}

func runUpdate(ctx context.Context, dryRun, asJSON bool, flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) error {
	path := dependencies.subsPath()
	file, err := LoadSubscriptions(path)
	if err != nil {
		return err
	}
	if len(file.Subscriptions) == 0 {
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(UpdateReport{})
			return nil
		}
		fmt.Fprintln(stdout, "no subscriptions; subscribe with: komiku-cli subscribe <series-url>")
		return nil
	}
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
	report := UpdateReport{Series: make([]UpdateSeries, 0, len(file.Subscriptions))}
	now := dependencies.now()

	for i := range file.Subscriptions {
		sub := &file.Subscriptions[i]
		entry := UpdateSeries{Slug: sub.Slug, SeriesURL: sub.SeriesURL}
		if err := ctx.Err(); err != nil {
			entry.Error = "interrupted"
			report.Series = append(report.Series, entry)
			break
		}
		result, err := updateOneSeries(ctx, sub, config, dependencies, dryRun, workers)
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.NewChapters = result.NewChapters
			entry.Downloaded = result.Downloaded
			entry.Skipped = result.Skipped
			entry.Failed = result.Failed
		}
		report.Series = append(report.Series, entry)
		sub.LastCheck = now
	}

	// Persist updated last-check timestamps (skip on dry run to keep the
	// check honest: a dry run should not count as a real poll).
	if !dryRun {
		if err := SaveSubscriptions(path, file); err != nil {
			// Non-fatal: the run still succeeded for the user.
			fmt.Fprintf(stdout, "warning: could not save subscription timestamps: %v\n", err)
		}
	}

	if asJSON {
		return json.NewEncoder(stdout).Encode(report)
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
func updateOneSeries(ctx context.Context, sub *Subscription, config Config, dependencies Dependencies, dryRun bool, workers int) (updateResult, error) {
	client := dependencies.newKomikuClient(config.ImageDelay)
	parsed, err := komiku.ValidateSeriesURL(sub.SeriesURL)
	if err != nil {
		return updateResult{}, err
	}
	if sub.Slug == "" {
		sub.Slug = SeriesSlug(parsed)
	}
	seriesStore, err := store.Open(config.OutputRoot, sub.Slug)
	if err != nil {
		return updateResult{}, err
	}
	chapters, err := client.Discover(ctx, sub.SeriesURL)
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
		if seriesStore.IsDone(chapter.Number) {
			continue
		}
		newChapters = append(newChapters, chapter.Display)
		jobs = append(jobs, Job{Chapter: chapter, Flat: true, Ambiguous: ambiguous[chapter.Number]})
	}
	result := updateResult{NewChapters: newChapters, Skipped: len(chapters) - len(jobs)}
	if dryRun || len(jobs) == 0 {
		return result, nil
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

// now returns the effective time, defaulting to wall clock.
func (d Dependencies) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// subsPath returns the subscriptions file path, honoring an injected override
// for tests, otherwise resolving via SubscriptionsPath().
func (d Dependencies) subsPath() string {
	if d.subscriptionsPath != "" {
		return d.subscriptionsPath
	}
	path, err := SubscriptionsPath()
	if err != nil {
		return ""
	}
	return path
}


// WithSubscriptionsPath returns a copy of Dependencies with the
// subscriptions file path overridden. It is a test seam.
func (d Dependencies) WithSubscriptionsPath(path string) Dependencies {
	d.subscriptionsPath = path
	return d
}
