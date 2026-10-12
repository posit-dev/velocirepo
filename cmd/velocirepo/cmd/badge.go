package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/posit-dev/velocirepo/internal/badge"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/spf13/cobra"
)

func badgeCmd() *cobra.Command {
	var (
		project    string
		output     string
		style      string
		color      string
		labelColor string
		label      string
		height     int
		radius     int
		query      string
	)

	cmd := &cobra.Command{
		Use:   "badge <type>",
		Short: "Generate SVG badges from metrics",
		Long: `Generate shields.io-style SVG badges. Available types: stars, forks, downloads, pageviews, custom.

For custom badges, provide --query and --label.`,
		Args:      cobra.ExactArgs(1),
		GroupID:   "badge",
		ValidArgs: []string{"stars", "forks", "downloads", "pageviews", "custom"},
		RunE: func(cmd *cobra.Command, args []string) error {
			badgeType := args[0]
			if project != "" {
				if _, err := cfg.GetProject(project); err != nil {
					return err
				}
			}

			if badgeType == "custom" {
				if query == "" {
					return fmt.Errorf("--query is required for custom badges")
				}
				if label == "" {
					return fmt.Errorf("--label is required for custom badges")
				}

				q := query
				if project != "" {
					q = fmt.Sprintf("SELECT * FROM (%s) AS velocirepo_badge_query WHERE project = %s", q, store.SQLStringLiteral(project))
				}

				msg, err := queryBadgeValue(q)
				if err != nil {
					return err
				}

				if color == "" {
					color = "#007ec6"
				}

				return writeBadge(cmd.OutOrStdout(), badge.Options{
					Label:      label,
					Message:    msg,
					Color:      color,
					LabelColor: labelColor,
					Style:      badge.Style(style),
					Height:     height,
					Radius:     radius,
				}, output)
			}

			preset, ok := badge.Presets[badgeType]
			if !ok {
				return fmt.Errorf("unknown badge type %q (available: stars, forks, downloads, pageviews, custom)", badgeType)
			}

			projectLit := ""
			if project != "" {
				projectLit = store.SQLStringLiteral(project)
			}
			q := preset.BuildQuery(projectLit)

			msg, err := queryBadgeValue(q)
			if err != nil {
				return err
			}

			if label == "" {
				label = preset.Label
			}
			if color == "" {
				color = preset.Color
			}

			return writeBadge(cmd.OutOrStdout(), badge.Options{
				Label:      label,
				Message:    msg,
				Color:      color,
				LabelColor: labelColor,
				Style:      badge.Style(style),
				Height:     height,
				Radius:     radius,
			}, output)
		},
	}

	cmd.Flags().StringVar(&query, "query", "", "SQL query returning a single value (for custom type)")
	cmd.Flags().StringVar(&project, "project", "", "scope to a specific project")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file (default: stdout)")
	cmd.Flags().StringVar(&style, "style", "flat", "badge style: flat, flat-square, plastic")
	cmd.Flags().StringVar(&color, "color", "", "message background color")
	cmd.Flags().StringVar(&labelColor, "label-color", "#555", "label background color")
	cmd.Flags().StringVar(&label, "label", "", "override label text")
	cmd.Flags().IntVar(&height, "height", 0, "badge height in pixels (0 = style default)")
	cmd.Flags().IntVar(&radius, "radius", -1, "corner radius (-1 = style default)")

	return cmd
}

func queryBadgeValue(query string) (string, error) {
	results, _, err := store.QueryLive(cfg.DataDir(), projectInfos(), indicatorDefs(), query)
	if err != nil {
		return "", fmt.Errorf("badge query: %w", err)
	}
	if len(results) == 0 {
		return "0", nil
	}

	for _, v := range results[0] {
		return badge.FormatValue(v), nil
	}
	return "0", nil
}

func writeBadge(out io.Writer, opts badge.Options, output string) error {
	svg := badge.Render(opts)

	if output == "" {
		_, _ = fmt.Fprint(out, svg)
		return nil
	}

	if err := os.WriteFile(output, []byte(svg), 0644); err != nil {
		return fmt.Errorf("write badge: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Badge written to %s\n", output)
	return nil
}
