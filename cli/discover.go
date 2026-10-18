package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bangadam/komiku-cli/komiku"
)

// FeedReport is the headless shape of the latest-chapter and newest-title
// feeds.
type FeedReport struct {
	Order  string                   `json:"order"`
	Type   string                   `json:"type,omitempty"`
	Genre  string                   `json:"genre,omitempty"`
	Genre2 string                   `json:"genre2,omitempty"`
	Page   int                      `json:"page"`
	Series []komiku.DiscoverySeries `json:"series"`
}

// FeedOptions is the CLI-level shape of one catalogue page request.
type FeedOptions struct {
	Order  string
	Type   string
	Genre  string
	Genre2 string
	Page   int
}

func writeJSON(stdout io.Writer, value any) error {
	return json.NewEncoder(stdout).Encode(value)
}

func NewRankingCommand(dependencies Dependencies) *cobra.Command {
	var period string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ranking",
		Short: "Show the Komiku ranking boards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRanking(cmd.Context(), period, asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().StringVar(&period, "period", "harian", "ranking period: harian, mingguan, bulanan or total")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}

func runRanking(ctx context.Context, period string, asJSON bool, stdout io.Writer, dependencies Dependencies) error {
	client := dependencies.newKomikuClient(0)
	series, err := client.Ranking(ctx, period)
	if err != nil {
		return fmt.Errorf("ranking: %w", err)
	}
	if asJSON {
		return writeJSON(stdout, map[string]any{"period": period, "series": series})
	}
	for _, entry := range series {
		fmt.Fprintf(stdout, "%3d. %s  %s  %d views  %s\n", entry.Rank, entry.Title, strings.Join(entry.Genres, ", "), entry.Views, entry.Chapter)
	}
	return nil
}

func NewTrendingCommand(dependencies Dependencies) *cobra.Command {
	var period string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "trending",
		Short: "Show the realtime trending titles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrending(cmd.Context(), period, limit, asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().StringVar(&period, "period", "24h", "trending window: 24h, 7d, 30d or active")
	cmd.Flags().IntVar(&limit, "limit", 40, "maximum number of titles")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}

func runTrending(ctx context.Context, period string, limit int, asJSON bool, stdout io.Writer, dependencies Dependencies) error {
	client := dependencies.newKomikuClient(0)
	report, err := client.Trending(ctx, period, limit)
	if err != nil {
		return fmt.Errorf("trending: %w", err)
	}
	if asJSON {
		return writeJSON(stdout, report)
	}
	for _, entry := range report.Series {
		fmt.Fprintf(stdout, "%3d. %s  %s  %d views\n", entry.Rank, entry.Title, entry.Type, entry.Views)
	}
	return nil
}

func newFeedCommand(dependencies Dependencies, use, short, order string) *cobra.Command {
	var kind, genre, genre2 string
	var page int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeed(cmd.Context(), FeedOptions{
				Order:  order,
				Type:   kind,
				Genre:  genre,
				Genre2: genre2,
				Page:   page,
			}, asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().StringVar(&kind, "type", "", "filter by type: manga, manhwa or manhua")
	cmd.Flags().StringVar(&genre, "genre", "", "filter by genre slug, e.g. action")
	cmd.Flags().StringVar(&genre2, "genre2", "", "combine a second genre slug")
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}

// NewLatestCommand lists the latest chapter updates.
func NewLatestCommand(dependencies Dependencies) *cobra.Command {
	return newFeedCommand(dependencies, "latest", "Show the latest chapter updates", "update")
}

// NewNewCommand lists the newest titles in the catalogue.
func NewNewCommand(dependencies Dependencies) *cobra.Command {
	return newFeedCommand(dependencies, "new", "Show the newest titles added to the catalogue", "new")
}

// NewRandomCommand lists a random selection of titles.
func NewRandomCommand(dependencies Dependencies) *cobra.Command {
	return newFeedCommand(dependencies, "random", "Show a random selection of titles", "random")
}

func runFeed(ctx context.Context, options FeedOptions, asJSON bool, stdout io.Writer, dependencies Dependencies) error {
	client := dependencies.newKomikuClient(0)
	series, err := client.Browse(ctx, komiku.FeedOptions{
		Order:  options.Order,
		Type:   options.Type,
		Genre:  options.Genre,
		Genre2: options.Genre2,
		Page:   options.Page,
	})
	if err != nil {
		return fmt.Errorf("%s feed: %w", options.Order, err)
	}
	report := FeedReport{
		Order:  options.Order,
		Type:   options.Type,
		Genre:  options.Genre,
		Genre2: options.Genre2,
		Page:   options.Page,
		Series: series,
	}
	if asJSON {
		return writeJSON(stdout, report)
	}
	for _, entry := range series {
		fmt.Fprintf(stdout, "%s  %s  %s  %s\n", entry.Title, entry.Type, strings.Join(entry.Genres, ", "), entry.Updated)
	}
	return nil
}

func NewCatalogCommand(dependencies Dependencies) *cobra.Command {
	var letter, kind, status string
	var page int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Browse the A–Z comic catalogue",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCatalog(cmd.Context(), letter, kind, status, page, asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().StringVar(&letter, "letter", "", "filter by initial letter, e.g. A or #")
	cmd.Flags().StringVar(&kind, "type", "", "filter by type: manga, manhwa or manhua")
	cmd.Flags().StringVar(&status, "status", "", "filter by status, e.g. ongoing or tamat")
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}

func runCatalog(ctx context.Context, letter, kind, status string, page int, asJSON bool, stdout io.Writer, dependencies Dependencies) error {
	client := dependencies.newKomikuClient(0)
	report, err := client.Catalog(ctx, letter, kind, status, page)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	report.Letter = letter
	report.Type = kind
	report.Status = status
	if asJSON {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "page %d/%d  total=%d\n", report.Page, report.TotalPages, report.Total)
	for _, entry := range report.Series {
		fmt.Fprintf(stdout, "%s  %s  %s  %s\n", entry.Title, entry.Type, strings.Join(entry.Genres, ", "), entry.Status)
	}
	return nil
}

func NewGenresCommand(dependencies Dependencies) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "genres",
		Short: "List every genre and its title count",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client := dependencies.newKomikuClient(0)
			genres, err := client.Genres(cmd.Context())
			if err != nil {
				return fmt.Errorf("genres: %w", err)
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"genres": genres})
			}
			for _, genre := range genres {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d\n", genre.Slug, genre.Name, genre.Count)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}

func NewGenreCommand(dependencies Dependencies) *cobra.Command {
	var sort, kind string
	var page int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "genre <name>",
		Short: "List titles in one genre",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
				return errors.New("genre requires exactly one genre name")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			client := dependencies.newKomikuClient(0)
			series, err := client.GenreSeries(cmd.Context(), args[0], sort, kind, page)
			if err != nil {
				return fmt.Errorf("genre %s: %w", args[0], err)
			}
			report := FeedReport{Order: sort, Type: kind, Page: page, Series: series}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"genre": args[0], "page": page, "series": series})
			}
			_ = report
			for _, entry := range series {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", entry.Title, entry.Type, strings.Join(entry.Genres, ", "), entry.Chapter)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sort, "sort", "update", "sort order: update, date, rating or random")
	cmd.Flags().StringVar(&kind, "type", "", "filter by type: manga, manhwa or manhua")
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print results as JSON")
	return cmd
}
