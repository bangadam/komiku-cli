package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
)

type stringFlag struct {
	value string
	set   bool
}

func (f *stringFlag) String() string { return f.value }
func (f *stringFlag) Set(value string) error {
	f.value, f.set = value, true
	return nil
}

type durationFlag struct {
	value time.Duration
	set   bool
}

func (f *durationFlag) String() string { return f.value.String() }
func (f *durationFlag) Set(value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	f.value, f.set = duration, true
	return nil
}

type Dependencies struct {
	HTTP       *http.Client
	Now        func() time.Time
	ConfigPath string
}

func Main(ctx context.Context, args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: komiku-cli dl <series-url> [--ch RANGE | --vol RANGE] --no-tui [--flat] [--pack --preset medium|small|tiny|raw]")
		fmt.Fprintln(stderr, packCommandUsage)
		fmt.Fprintln(stderr, "usage: komiku-cli config [--out DIR]")
		return 2
	}
	var err error
	switch args[0] {
	case "dl":
		err = runDownload(ctx, args[1:], stdout, dependencies)
	case "pack":
		err = runPack(ctx, args[1:], stdout, dependencies)
	case "config":
		err = runConfig(args[1:], stdout, dependencies)
	default:
		fmt.Fprintf(stderr, "unknown command %q; expected dl, pack, or config\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func runDownload(ctx context.Context, args []string, stdout io.Writer, dependencies Dependencies) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: komiku-cli dl <series-url> [--ch RANGE | --vol RANGE] --no-tui [--flat] [--pack --preset medium|small|tiny|raw]")
	}
	seriesURL := args[0]
	flags := flag.NewFlagSet("dl", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var chapterExpression, volumeExpression string
	var noTUI, flat, pack bool
	var output, delay, preset stringFlag
	var workers int
	flags.StringVar(&chapterExpression, "ch", "", "chapter list/range")
	flags.StringVar(&volumeExpression, "vol", "", "volume list/range")
	flags.BoolVar(&noTUI, "no-tui", false, "run headless")
	flags.BoolVar(&flat, "flat", false, "store chapters without volume folders")
	flags.BoolVar(&pack, "pack", false, "pack selected volumes after download")
	flags.Var(&output, "out", "output directory")
	flags.Var(&delay, "delay", "image request delay")
	flags.Var(&preset, "preset", "pack preset")
	flags.IntVar(&workers, "workers", 3, "chapter worker count")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument %q", flags.Arg(0))
	}
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
	if output.set {
		overrides.OutputRoot = &output.value
	}
	if delay.set {
		parsedDelay, err := time.ParseDuration(delay.value)
		if err != nil {
			return fmt.Errorf("invalid --delay: %w", err)
		}
		overrides.ImageDelay = &parsedDelay
	}
	if preset.set {
		overrides.Preset = &preset.value
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
		selected, err := SelectChapters(chapters, chapterExpression)
		if err != nil {
			return err
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
	failed := false
	for _, result := range results {
		fmt.Fprintf(stdout, "chapter %s: %s pages=%d/%d\n", result.Chapter.Display, result.Label(), result.Success, result.Total)
		if result.Status != Done {
			failed = true
		}
	}
	fmt.Fprintf(stdout, "summary DONE=%d PART=%d FAIL=%d NOIMG=%d log=%s\n", summary.Counts[Done], summary.Counts[Part], summary.Counts[Fail], summary.Counts[NoImg], summary.AuditPath)
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

func runConfig(args []string, stdout io.Writer, dependencies Dependencies) error {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var output stringFlag
	flags.Var(&output, "out", "persistent output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument %q", flags.Arg(0))
	}
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
	if output.set {
		fileConfig.OutputRoot = output.value
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
