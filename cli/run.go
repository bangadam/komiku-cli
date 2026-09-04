package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
)

const dlCommandUsage = "usage: komiku-cli dl <series-url> [--ch RANGE|all|missing|latest:N | --vol RANGE] --no-tui [--flat] [--pack --preset medium|small|tiny|raw]"

// Version is reported by --version. Binary builds fall back to "devel".
const Version = "0.2.0"

// VersionString prefers the module version recorded by the Go toolchain.
func VersionString() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}

// exitCodeError carries an already-reported exit code past cobra without a
// second "error:" line.
type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string { return fmt.Sprintf("exit %d", e.code) }

type Dependencies struct {
	HTTP             *http.Client
	Now              func() time.Time
	ConfigPath       string
	BaseURL          string
	subscriptionsPath string
}

func Main(ctx context.Context, args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	root := NewRootCommand(stdout, stderr, dependencies)
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		var coded exitCodeError
		if errors.As(err, &coded) {
			return coded.code
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func printHeadlessUsage(stderr io.Writer) {
	fmt.Fprintln(stderr, dlCommandUsage)
	fmt.Fprintln(stderr, packCommandUsage)
	fmt.Fprintln(stderr, "usage: komiku-cli config [--out DIR]")
	fmt.Fprintln(stderr, "usage: komiku-cli search <query> [--json]")
	fmt.Fprintln(stderr, "usage: komiku-cli info <series-url> [--out DIR] [--json]")
}

func NewRootCommand(stdout, stderr io.Writer, dependencies Dependencies) *cobra.Command {
	root := &cobra.Command{
		Use:           "komiku-cli",
		Short:         "Keyboard-first Komiku manga downloader and offline CBZ packer",
		Version:       VersionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "unknown command %q; expected tui, dl, pack, verify, serve, subscribe, unsubscribe, subs, update, config, search, or info\n", args[0])
			return exitCodeError{2}
		}
		printHeadlessUsage(cmd.ErrOrStderr())
		return exitCodeError{2}
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(NewDownloadCommand(dependencies))
	root.AddCommand(NewPackCommand(dependencies))
	root.AddCommand(NewVerifyCommand())
	root.AddCommand(NewServeCommand(dependencies))
	root.AddCommand(NewSubscribeCommand(dependencies))
	root.AddCommand(NewUnsubscribeCommand(dependencies))
	root.AddCommand(NewSubsCommand(dependencies))
	root.AddCommand(NewUpdateCommand(dependencies))
	root.AddCommand(NewLibraryCommand(dependencies))
	root.AddCommand(NewConfigCommand(dependencies))
	root.AddCommand(NewSearchCommand(dependencies))
	root.AddCommand(NewInfoCommand(dependencies))
	return root
}

// exactOneArg rejects the old manual flag rules: no positional before the
// series argument, exactly one series argument.
func exactOneArg(usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return errors.New(usage)
		}
		if len(args) > 1 {
			return fmt.Errorf("unexpected positional argument %q", args[1])
		}
		return nil
	}
}

func NewDownloadCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dl <series-url>",
		Short: "Download chapters headlessly into the download store",
		Args:  exactOneArg(dlCommandUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDownload(cmd.Context(), args[0], cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().String("ch", "", "chapter list/range, all, missing, or latest:N")
	cmd.Flags().String("vol", "", "volume list/range")
	cmd.Flags().Bool("no-tui", false, "run headless")
	cmd.Flags().Bool("flat", false, "store chapters without volume folders")
	cmd.Flags().Bool("pack", false, "pack selected volumes after download")
	cmd.Flags().Bool("json", false, "print the download report as JSON")
	cmd.Flags().String("out", "", "output directory")
	cmd.Flags().String("delay", "", "image request delay")
	cmd.Flags().String("preset", "", "pack preset")
	cmd.Flags().Int("workers", 3, "chapter worker count")
	return cmd
}

func runDownload(ctx context.Context, seriesURL string, flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) error {
	chapterExpression, _ := flags.GetString("ch")
	volumeExpression, _ := flags.GetString("vol")
	noTUI, _ := flags.GetBool("no-tui")
	flat, _ := flags.GetBool("flat")
	pack, _ := flags.GetBool("pack")
	output, _ := flags.GetString("out")
	delay, _ := flags.GetString("delay")
	preset, _ := flags.GetString("preset")
	workers, _ := flags.GetInt("workers")
	if !noTUI {
		return errors.New("issues 01-13 support headless mode only; pass --no-tui")
	}
	if workers <= 0 || workers > 32 {
		return errors.New("workers must be between 1 and 32")
	}
	if chapterExpression == "" && volumeExpression == "" {
		return errors.New("one of --ch or --vol is required in headless mode")
	}
	if chapterExpression != "" && volumeExpression != "" {
		return errors.New("--ch and --vol cannot be combined")
	}
	if pack && volumeExpression == "" {
		return errors.New("--pack requires --vol with a mapped volume selection")
	}
	parsedURL, err := komiku.ValidateSeriesURL(seriesURL)
	if err != nil {
		return err
	}
	configPath := dependencies.ConfigPath
	if configPath == "" {
		configPath, err = ConfigPath()
		if err != nil {
			return err
		}
	}
	fileConfig, err := LoadFileConfig(configPath)
	if err != nil {
		return err
	}
	overrides := Overrides{}
	if flags.Changed("out") {
		overrides.OutputRoot = &output
	}
	if flags.Changed("delay") {
		parsedDelay, err := time.ParseDuration(delay)
		if err != nil {
			return fmt.Errorf("invalid --delay: %w", err)
		}
		overrides.ImageDelay = &parsedDelay
	}
	if flags.Changed("preset") {
		overrides.Preset = &preset
	}
	config, err := ResolveConfig(fileConfig, overrides)
	if err != nil {
		return err
	}
	series := SeriesSlug(parsedURL)
	if series == "" {
		return fmt.Errorf("cannot derive series directory from URL %q", seriesURL)
	}
	seriesStore, err := store.Open(config.OutputRoot, series)
	if err != nil {
		return err
	}
	client := komiku.NewClient(dependencies.HTTP, config.ImageDelay)
	chapters, err := client.Discover(ctx, seriesURL)
	if err != nil {
		return fmt.Errorf("discover chapters: %w", err)
	}
	if len(chapters) == 0 {
		return errors.New("series discovery returned no chapter hrefs")
	}
	ambiguous := ambiguousChapterNumbers(chapters)
	var jobs []Job
	var selectedVolumes []komiku.Volume
	if chapterExpression != "" {
		selected, err := selectChaptersForDownload(chapters, chapterExpression, seriesStore)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			fmt.Fprintf(stdout, "no missing chapters: %s is already complete (%d done)\n", series, seriesStoreCountDone(seriesStore))
			return nil
		}
		jobs = make([]Job, 0, len(selected))
		for _, chapter := range selected {
			jobs = append(jobs, Job{Chapter: chapter, Flat: true, Ambiguous: ambiguous[chapter.Number]})
		}
	} else {
		if flat {
			return errors.New("--flat is used with --ch; --vol requires a volume mapping")
		}
		volumes, err := LoadVolumeMapping(ctx, client, seriesStore.Dir(), series, chapters)
		if err != nil {
			return err
		}
		jobs, err = SelectVolumes(chapters, volumes, volumeExpression)
		if err != nil {
			return err
		}
		selectedVolumes, err = SelectedVolumeMappings(volumes, volumeExpression)
		if err != nil {
			return err
		}
		for i := range jobs {
			jobs[i].Ambiguous = ambiguous[jobs[i].Chapter.Number]
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("download interrupted: %w", err)
	}
	engine := Engine{Client: client, Store: seriesStore, Workers: workers, Now: dependencies.Now}
	summary := engine.Run(ctx, jobs)
	if summary.Err != nil {
		return summary.Err
	}
	results := summary.Results
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("download interrupted after %d/%d chapter results: %w", len(results), len(jobs), err)
	}
	if len(results) != len(jobs) {
		return fmt.Errorf("download stopped after %d/%d chapter results", len(results), len(jobs))
	}
	asJSON, _ := flags.GetBool("json")
	failed := false
	for _, result := range results {
		if result.Status != Done {
			failed = true
			break
		}
	}
	if asJSON {
		if err := writeDownloadReportJSON(stdout, series, seriesURL, summary, selectedVolumes); err != nil {
			return err
		}
	} else {
		for _, result := range results {
			fmt.Fprintf(stdout, "chapter %s: %s pages=%d/%d\n", result.Chapter.Display, result.Label(), result.Success, result.Total)
		}
		fmt.Fprintf(stdout, "summary DONE=%d PART=%d FAIL=%d NOIMG=%d log=%s\n", summary.Counts[Done], summary.Counts[Part], summary.Counts[Fail], summary.Counts[NoImg], summary.AuditPath)
	}
	if failed {
		return errors.New("batch completed with partial or failed chapters")
	}
	if len(selectedVolumes) > 0 {
		if err := RecordPackManifest(seriesStore.Dir(), series, seriesURL, "download-mapping", selectedVolumes, jobs, results); err != nil {
			return fmt.Errorf("save pack manifest: %w", err)
		}
	}
	if pack {
		plan := PreparePack(seriesStore, series, packer.Preset(config.Preset), selectedVolumes, jobs, results)
		if len(plan.Skipped) > 0 {
			return fmt.Errorf("volume %02d cannot be packed: %s", plan.Skipped[0].Volume, plan.Skipped[0].Reason)
		}
		outcomes, packErr := PackPreparedVolumes(ctx, plan)
		var volumeErr error
		for _, outcome := range outcomes {
			if outcome.Err != nil {
				fmt.Fprintf(stdout, "pack failed: volume %02d: %v\n", outcome.Volume, outcome.Err)
				if volumeErr == nil {
					volumeErr = fmt.Errorf("pack volume %02d: %w", outcome.Volume, outcome.Err)
				}
				continue
			}
			fmt.Fprintf(stdout, "packed: %s preset=%s\n", outcome.Result.Path, outcome.Result.Preset)
			for _, warning := range outcome.Result.Warnings {
				fmt.Fprintf(stdout, "warning: volume %02d: %s\n", outcome.Volume, warning)
			}
		}
		if packErr != nil {
			return packErr
		}
		if volumeErr != nil {
			return volumeErr
		}
	}
	return nil
}

// selectChaptersForDownload supports the special selectors all, missing, and
// latest:N on top of the explicit chapter expression syntax.
func selectChaptersForDownload(chapters []komiku.Chapter, expression string, seriesStore *store.SeriesStore) ([]komiku.Chapter, error) {
	switch {
	case strings.TrimSpace(expression) == "all":
		return append([]komiku.Chapter(nil), chapters...), nil
	case strings.TrimSpace(expression) == "missing":
		missing := make([]komiku.Chapter, 0, len(chapters))
		for _, chapter := range chapters {
			if !seriesStore.IsDone(chapter.Number) {
				missing = append(missing, chapter)
			}
		}
		return missing, nil
	case strings.HasPrefix(strings.TrimSpace(expression), "latest:"):
		countText := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expression), "latest:"))
		count, err := strconv.Atoi(countText)
		if err != nil || count < 1 {
			return nil, fmt.Errorf("invalid latest selector %q; expected latest:N with N >= 1", expression)
		}
		if count > len(chapters) {
			count = len(chapters)
		}
		return append([]komiku.Chapter(nil), chapters[len(chapters)-count:]...), nil
	default:
		return SelectChapters(chapters, expression)
	}
}

func seriesStoreCountDone(seriesStore *store.SeriesStore) int {
	done, err := store.ReadDone(seriesStore.Root, seriesStore.Series)
	if err != nil {
		return 0
	}
	return len(done)
}

type DownloadReport struct {
	Series         string         `json:"series"`
	SeriesURL      string         `json:"series_url,omitempty"`
	Requested      int            `json:"requested"`
	Started        int            `json:"started"`
	PagesOK        int            `json:"pages_ok"`
	PagesFailed    int            `json:"pages_failed"`
	Counts         map[Status]int `json:"counts"`
	AuditPath      string         `json:"audit_log"`
	Results        []ResultReport `json:"results"`
	MappedVolumes  []int          `json:"mapped_volumes,omitempty"`
	FailedChapters []string       `json:"failed_chapters,omitempty"`
}

type ResultReport struct {
	Chapter    string   `json:"chapter"`
	Status     Status   `json:"status"`
	PagesOK    int      `json:"pages_ok"`
	PagesTotal int      `json:"pages_total"`
	Errors     []string `json:"errors,omitempty"`
}

func writeDownloadReportJSON(stdout io.Writer, series, seriesURL string, summary Summary, volumes []komiku.Volume) error {
	report := DownloadReport{
		Series:        series,
		SeriesURL:     seriesURL,
		Requested:     summary.Requested,
		Started:       summary.Started,
		PagesOK:       summary.PagesOK,
		PagesFailed:   summary.PagesFailed,
		Counts:        summary.Counts,
		AuditPath:     summary.AuditPath,
		Results:       make([]ResultReport, 0, len(summary.Results)),
	}
	for _, volume := range volumes {
		report.MappedVolumes = append(report.MappedVolumes, volume.Volume)
	}
	for _, result := range summary.Results {
		report.Results = append(report.Results, ResultReport{
			Chapter:    result.Chapter.Display,
			Status:     result.Status,
			PagesOK:    result.Success,
			PagesTotal: result.Total,
			Errors:     result.Errors,
		})
		if result.Status != Done {
			report.FailedChapters = append(report.FailedChapters, result.Chapter.Display)
		}
	}
	if report.Counts == nil {
		report.Counts = map[Status]int{}
	}
	return json.NewEncoder(stdout).Encode(report)
}

func SelectedVolumeMappings(volumes []komiku.Volume, expression string) ([]komiku.Volume, error) {
	wanted, err := parseIntegerSelection(expression)
	if err != nil {
		return nil, err
	}
	selected := make([]komiku.Volume, 0, len(wanted))
	for _, volume := range volumes {
		if wanted[volume.Volume] {
			selected = append(selected, volume)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Volume < selected[j].Volume })
	if len(selected) != len(wanted) {
		return nil, errors.New("selected volume is missing from mapping")
	}
	return selected, nil
}

func validateMappedChapters(mapping komiku.Volume, jobs []Job) error {
	discovered := make(map[int]bool, len(jobs))
	for _, job := range jobs {
		whole, err := strconv.Atoi(strings.SplitN(job.Chapter.Display, ".", 2)[0])
		if err == nil && job.Chapter.Number == float64(whole) {
			discovered[whole] = true
		}
	}
	for chapter := mapping.Start; chapter <= mapping.End; chapter++ {
		if !discovered[chapter] {
			return fmt.Errorf("volume %02d requires chapter %d, but it is not selected or discovered", mapping.Volume, chapter)
		}
	}
	return nil
}

func LoadVolumeMapping(ctx context.Context, client *komiku.Client, seriesDir, series string, chapters []komiku.Chapter) ([]komiku.Volume, error) {
	cachePath := filepath.Join(seriesDir, ".volumes.json")
	maxChapter := komiku.MaxDiscoveredChapter(chapters)
	data, err := os.ReadFile(cachePath)
	if err == nil {
		cache, err := komiku.DecodeVolumeCache(data, maxChapter)
		if err != nil {
			return nil, err
		}
		return cache.Volumes, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read volume cache: %w", err)
	}
	cache, err := client.FetchVolumeMapping(ctx, strings.ReplaceAll(series, "-", " "), maxChapter)
	if err != nil {
		return nil, fmt.Errorf("%w; create/edit %s or use --ch ... --flat", err, cachePath)
	}
	if err := store.WriteJSONAtomic(cachePath, cache); err != nil {
		return nil, fmt.Errorf("write volume cache: %w", err)
	}
	return cache.Volumes, nil
}

func SeriesSlug(seriesURL *url.URL) string {
	parts := strings.Split(strings.Trim(seriesURL.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	name := parts[len(parts)-1]
	if name == "manga" || name == "" {
		return ""
	}
	return name
}

func ambiguousChapterNumbers(chapters []komiku.Chapter) map[float64]bool {
	firstRaw := make(map[float64]string, len(chapters))
	ambiguous := make(map[float64]bool)
	for _, chapter := range chapters {
		if raw, exists := firstRaw[chapter.Number]; exists && raw != chapter.RawID {
			ambiguous[chapter.Number] = true
		} else {
			firstRaw[chapter.Number] = chapter.RawID
		}
	}
	return ambiguous
}

func countStatuses(results []Result) map[Status]int {
	counts := make(map[Status]int)
	for _, result := range results {
		counts[result.Status]++
	}
	return counts
}

func ExitStatus(args []string) int {
	return Main(context.Background(), args, os.Stdout, os.Stderr, Dependencies{})
}

func ParseExitCode(value string) (int, error) {
	return strconv.Atoi(value)
}

func NewConfigCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or update the persistent download location",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfig(cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().String("out", "", "persistent output directory")
	return cmd
}

func runConfig(flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) error {
	configPath := dependencies.ConfigPath
	var err error
	if configPath == "" {
		configPath, err = ConfigPath()
		if err != nil {
			return err
		}
	}
	fileConfig, err := LoadFileConfig(configPath)
	if err != nil {
		return err
	}
	if flags.Changed("out") {
		output, _ := flags.GetString("out")
		fileConfig.OutputRoot = output
		if err := SaveFileConfig(configPath, fileConfig); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
	}
	config, err := ResolveConfig(fileConfig, Overrides{})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "output root: %s\nconfig: %s\n", config.OutputRoot, configPath)
	return nil
}
