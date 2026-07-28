package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/posit-dev/velocirepo/internal/ui"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	verbose bool
	quiet   bool
	cfg     *config.Config
)

const (
	requiresConfigAnnotation = "velocirepo/requires-config"
	requiresSchemaAnnotation = "velocirepo/requires-schema"
)

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "velocirepo",
		Short: "Track your open-source project's pulse across package registries, GitHub, and the web",
		Long:  "velocirepo tracks your open-source project's pulse across package registries, GitHub, and the web — collecting daily metrics from GitHub, PyPI, CRAN, Homebrew, Plausible, OpenVSX, and YouTube.",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			setupLogging()
			ui.SetQuiet(quiet)

			if !commandRequiresConfig(cmd) {
				return nil
			}

			var err error
			cfg, err = config.Load(cfgFile)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			_ = godotenv.Load(filepath.Join(cfg.Dir, ".env"))

			if commandRequiresSchema(cmd) {
				if err := store.CheckSchemaVersion(cfg.DataDir()); err != nil {
					return err
				}
			}

			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: walk up for velocirepo.toml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")

	rootCmd.AddGroup(
		&cobra.Group{ID: "fetch", Title: "Fetching:"},
		&cobra.Group{ID: "query", Title: "Querying:"},
		&cobra.Group{ID: "badge", Title: "Badges:"},
		&cobra.Group{ID: "view", Title: "Views:"},
		&cobra.Group{ID: "project", Title: "Projects:"},
		&cobra.Group{ID: "ci", Title: "CI/CD:"},
		&cobra.Group{ID: "data", Title: "Data:"},
	)

	// Fetching
	rootCmd.AddCommand(fetchAllCmd())
	for _, def := range fetchSources {
		rootCmd.AddCommand(makeFetchCmd(def))
	}

	// Querying
	rootCmd.AddCommand(queryCmd())
	rootCmd.AddCommand(schemaCmd())
	rootCmd.AddCommand(exportCmd())
	rootCmd.AddCommand(showIndicatorsCmd())

	// Badges
	rootCmd.AddCommand(badgeCmd())

	// Projects
	init := initCmd()
	setCommandAnnotation(init, requiresConfigAnnotation, "false")
	rootCmd.AddCommand(init)
	rootCmd.AddCommand(addProjectCmd())
	rootCmd.AddCommand(removeProjectCmd())
	rootCmd.AddCommand(renameProjectCmd())
	rootCmd.AddCommand(updateProjectCmd())
	rootCmd.AddCommand(showProjectCmd())
	rootCmd.AddCommand(listProjectsCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(importProjectsCmd())
	rootCmd.AddCommand(validateProjectsCmd())

	// Views
	rootCmd.AddCommand(addViewCmd())
	rootCmd.AddCommand(removeViewCmd())
	rootCmd.AddCommand(listViewsCmd())
	rootCmd.AddCommand(showViewCmd())
	rootCmd.AddCommand(renderViewCmd())
	rootCmd.AddCommand(renderViewsCmd())
	rootCmd.AddCommand(serveViewCmd())
	rootCmd.AddCommand(setupViewsCmd())

	// CI/CD
	rootCmd.AddCommand(installCICmd())
	rootCmd.AddCommand(syncSecretsCmd())

	// Data
	migrate := migrateCmd()
	setCommandAnnotation(migrate, requiresSchemaAnnotation, "false")
	rootCmd.AddCommand(migrate)
	rootCmd.AddCommand(buildDBCmd())
	rootCmd.AddCommand(validateDataCmd())

	// MCP
	mcp := mcpCmd()
	setCommandAnnotation(mcp, requiresSchemaAnnotation, "false")
	rootCmd.AddCommand(mcp)

	// Auth
	auth := authCmd()
	setCommandAnnotation(auth, requiresSchemaAnnotation, "false")
	rootCmd.AddCommand(auth)

	// Other
	version := versionCmd()
	setCommandAnnotation(version, requiresConfigAnnotation, "false")
	rootCmd.AddCommand(version)

	return rootCmd
}

func setCommandAnnotation(cmd *cobra.Command, key, value string) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[key] = value
}

func commandRequiresConfig(cmd *cobra.Command) bool {
	if isCompletionCmd(cmd) {
		return false
	}
	return commandAnnotationBool(cmd, requiresConfigAnnotation, true)
}

func commandRequiresSchema(cmd *cobra.Command) bool {
	return commandAnnotationBool(cmd, requiresSchemaAnnotation, true)
}

func commandAnnotationBool(cmd *cobra.Command, key string, defaultValue bool) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if value, ok := c.Annotations[key]; ok {
			return value != "false"
		}
	}
	return defaultValue
}

func isCompletionCmd(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "completion" || c.Name() == "__complete" {
			return true
		}
	}
	return false
}

func setupLogging() {
	level := slog.LevelError + 1 // suppress all by default
	if verbose {
		level = slog.LevelDebug
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}
