package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	packer "github.com/bangadam/komiku-cli/pack"
)

const packCommandUsage = "usage: komiku-cli pack <series-dir> [--vol LIST] [--preset medium|small|tiny|raw] [--recover-wikipedia [--wikipedia-title TITLE]] [--flat [--series NAME]]"

var executePreparedPack = PackPreparedVolumes

func NewPackCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pack <series-dir>",
		Short: "Pack downloaded chapters into CBZ archives",
		Args:  exactOneArg(packCommandUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPack(cmd.Context(), args[0], cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().String("vol", "", "volume list/range")
	cmd.Flags().String("preset", DefaultPreset, "pack preset")
	cmd.Flags().Bool("recover-wikipedia", false, "recover a legacy flat run from English Wikipedia")
	cmd.Flags().String("wikipedia-title", "", "English Wikipedia series title override")
	cmd.Flags().Bool("flat", false, "pack flat chapter directories into one CBZ without volume mapping")
	cmd.Flags().String("series", "", "series display name for --flat (defaults to the directory name)")
	return cmd
}

func runPack(ctx context.Context, seriesDir string, flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) error {
	volumeExpression, _ := flags.GetString("vol")
	presetName, _ := flags.GetString("preset")
	wikipediaTitle, _ := flags.GetString("wikipedia-title")
	recoverWikipedia, _ := flags.GetBool("recover-wikipedia")
	flat, _ := flags.GetBool("flat")
	seriesName, _ := flags.GetString("series")
	preset, err := parsePackPreset(presetName)
	if err != nil {
		return err
	}
	if !recoverWikipedia && wikipediaTitle != "" {
		return errors.New("--wikipedia-title requires --recover-wikipedia")
	}
	if flat && recoverWikipedia {
		return errors.New("--flat and --recover-wikipedia are mutually exclusive")
	}
	if flat && volumeExpression != "" {
		return errors.New("--vol requires a volume mapping; --flat has none")
	}
	if !flat && seriesName != "" {
		return errors.New("--series requires --flat")
	}

	return PackDownloaded(ctx, seriesDir, PackDownloadedOptions{
		VolumeExpression: volumeExpression,
		Preset:           preset,
		RecoverWikipedia: recoverWikipedia,
		WikipediaTitle:   wikipediaTitle,
		Flat:             flat,
		SeriesName:       seriesName,
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
	Flat             bool
	SeriesName       string
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
		createdArchives, err := plannedPackArchives(recovery.Plan)
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
	if options.Flat {
		plan, err := prepareFlatPack(seriesDir, options.SeriesName, options.Preset)
		if err != nil {
			return fmt.Errorf("flat pack: %w", err)
		}
		if err := validatePackCommandPlan(plan); err != nil {
			return err
		}
		createdArchives, err := plannedPackArchives(plan)
		if err != nil {
			return err
		}
		if err := executePackCommand(ctx, stdout, plan); err != nil {
			return errors.Join(err, removeCreatedArchives(createdArchives))
		}
		fmt.Fprintf(stdout, "flat pack wrote no .pack.json; rerun after new chapters to pack the wider range\n")
		return nil
	}

	plan, err := PrepareManifestPack(seriesDir, options.Preset, options.VolumeExpression)
	if err != nil {
		if errors.Is(err, errPackManifestNotFound) {
			return fmt.Errorf("%w; legacy flat download? recover once with: komiku-cli pack %q --recover-wikipedia, or pack without volumes: komiku-cli pack %q --flat", err, seriesDir, seriesDir)
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

func plannedPackArchives(plan PackPlan) ([]string, error) {
	created := make([]string, 0, len(plan.Volumes))
	seen := make(map[string]bool, len(plan.Volumes))
	for _, volume := range plan.Volumes {
		archiveName, err := volume.ArchiveName()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(volume.SeriesDir, archiveName)
		if seen[path] {
			return nil, fmt.Errorf("pack archive target is duplicated: %s", path)
		}
		seen[path] = true
		if _, err := os.Lstat(path); err == nil {
			return nil, fmt.Errorf("pack refuses to replace pre-existing archive: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect pack archive target %s: %w", path, err)
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
