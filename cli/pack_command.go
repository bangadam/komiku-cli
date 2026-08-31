package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	packer "github.com/bangadam/komiku-cli/pack"
)

const packCommandUsage = "usage: komiku-cli pack <series-dir> [--vol LIST] [--preset medium|small|tiny|raw] [--recover-wikipedia [--wikipedia-title TITLE]]"

var executePreparedPack = PackPreparedVolumes

func runPack(ctx context.Context, args []string, stdout io.Writer, dependencies Dependencies) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New(packCommandUsage)
	}
	seriesDir := args[0]
	flags := flag.NewFlagSet("pack", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var volumeExpression, presetName, wikipediaTitle string
	var recoverWikipedia bool
	flags.StringVar(&volumeExpression, "vol", "", "volume list/range")
	flags.StringVar(&presetName, "preset", DefaultPreset, "pack preset")
	flags.BoolVar(&recoverWikipedia, "recover-wikipedia", false, "recover a legacy flat run from English Wikipedia")
	flags.StringVar(&wikipediaTitle, "wikipedia-title", "", "English Wikipedia series title override")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument %q", flags.Arg(0))
	}
	preset, err := parsePackPreset(presetName)
	if err != nil {
		return err
	}
	if !recoverWikipedia && wikipediaTitle != "" {
		return errors.New("--wikipedia-title requires --recover-wikipedia")
	}

	return PackDownloaded(ctx, seriesDir, PackDownloadedOptions{
		VolumeExpression: volumeExpression,
		Preset:           preset,
		RecoverWikipedia: recoverWikipedia,
		WikipediaTitle:   wikipediaTitle,
		HTTP:             dependencies.HTTP,
		Output:           stdout,
	})
}

type PackDownloadedOptions struct {
	VolumeExpression string
	Preset           packer.Preset
	RecoverWikipedia bool
	RecoverComplete  bool
	WikipediaTitle   string
	HTTP             *http.Client
	Output           io.Writer
}

func PackDownloaded(ctx context.Context, seriesDir string, options PackDownloadedOptions) error {
	stdout := options.Output
	if stdout == nil {
		stdout = io.Discard
	}
	if options.RecoverWikipedia {
		recovery, err := prepareWikipediaRecovery(ctx, seriesDir, options.WikipediaTitle, options.VolumeExpression, options.Preset, options.HTTP, options.RecoverComplete)
		if err != nil {
			return fmt.Errorf("recover Wikipedia mapping: %w", err)
		}
		if err := validatePackCommandPlan(recovery.Plan); err != nil {
			return err
		}
		transaction, err := prepareRecoveredPackManifest(seriesDir, recovery.Series, recovery.Mappings, recovery.Sources)
		if err != nil {
			return fmt.Errorf("prepare recovered pack manifest: %w", err)
		}
		defer transaction.Abort()
		createdArchives, err := recoveryArchiveTransaction(recovery.Plan)
		if err != nil {
			return err
		}
		if err := executePackCommand(ctx, stdout, recovery.Plan); err != nil {
			return errors.Join(err, removeCreatedArchives(createdArchives))
		}
		if err := transaction.Commit(); err != nil {
			var cleanupErr error
			if !transaction.HasPublishedManifest() {
				cleanupErr = removeCreatedArchives(createdArchives)
			}
			return errors.Join(fmt.Errorf("save recovered pack manifest: %w", err), cleanupErr)
		}
		fmt.Fprintf(stdout, "Wikipedia source: %s title=%q\n", recovery.SourceURL, recovery.Title)
		if len(recovery.Ignored) > 0 {
			label := "ignored local chapters outside --vol"
			if options.RecoverComplete {
				label = "left unchanged because they are outside complete volumes"
			}
			fmt.Fprintf(stdout, "%s: %s\n", label, strings.Join(recovery.Ignored, ","))
		}
		fmt.Fprintf(stdout, "recovered manifest: %s\n", PackManifestPath(seriesDir))
		return nil
	}
	plan, err := PrepareManifestPack(seriesDir, options.Preset, options.VolumeExpression)
	if err != nil {
		if errors.Is(err, errPackManifestNotFound) {
			return fmt.Errorf("%w; legacy flat download? recover once with: komiku-cli pack %q --recover-wikipedia", err, seriesDir)
		}
		return err
	}
	if err := validatePackCommandPlan(plan); err != nil {
		return err
	}
	return executePackCommand(ctx, stdout, plan)
}

func parsePackPreset(value string) (packer.Preset, error) {
	switch packer.Preset(value) {
	case packer.Medium, packer.Small, packer.Tiny, packer.Raw:
		return packer.Preset(value), nil
	default:
		return "", fmt.Errorf("unknown preset %q; expected medium, small, tiny, or raw", value)
	}
}

func validatePackCommandPlan(plan PackPlan) error {
	if plan.DisabledReason != "" {
		return fmt.Errorf("pack disabled: %s", plan.DisabledReason)
	}
	if len(plan.Skipped) > 0 {
		return fmt.Errorf("volume %02d cannot be packed: %s", plan.Skipped[0].Volume, plan.Skipped[0].Reason)
	}
	if len(plan.Volumes) == 0 {
		return errors.New("pack plan has no complete volumes")
	}
	for _, volume := range plan.Volumes {
		root := volume.SourceRoot
		if root == "" {
			root = volume.SeriesDir
		}
		for _, chapter := range volume.Chapters {
			_, pages, err := validatePackSource(root, chapter.Dir)
			if err != nil {
				return fmt.Errorf("volume %02d chapter %s source: %w", volume.Number, chapter.Display, err)
			}
			if pages != chapter.ExpectedPages {
				return fmt.Errorf("volume %02d chapter %s has %d valid pages, expected %d", volume.Number, chapter.Display, pages, chapter.ExpectedPages)
			}
		}
	}
	return nil
}

func recoveryArchiveTransaction(plan PackPlan) ([]string, error) {
	created := make([]string, 0, len(plan.Volumes))
	seen := make(map[string]bool, len(plan.Volumes))
	for _, volume := range plan.Volumes {
		path := filepath.Join(volume.SeriesDir, fmt.Sprintf("%s Volume %02d.cbz", volume.Series, volume.Number))
		if seen[path] {
			return nil, fmt.Errorf("recovery archive target is duplicated: %s", path)
		}
		seen[path] = true
		if _, err := os.Lstat(path); err == nil {
			return nil, fmt.Errorf("recovery refuses to replace pre-existing archive: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect recovery archive target %s: %w", path, err)
		}
		created = append(created, path)
	}
	return created, nil
}

func removeCreatedArchives(paths []string) error {
	var cleanupErr error
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove recovery archive %s: %w", path, err))
		}
	}
	return cleanupErr
}

func executePackCommand(ctx context.Context, stdout io.Writer, plan PackPlan) error {
	outcomes, packErr := executePreparedPack(ctx, plan)
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
	return volumeErr
}
