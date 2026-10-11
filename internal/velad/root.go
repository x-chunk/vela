// Package velad implements the command line of the server binary: "serve"
// (the default) and the administrative subcommands that operators run inside
// the container with "docker exec vela velad ...".
//
// API keys and signing keys are managed only here, never over HTTP, so that
// no API key, however privileged, can mint new credentials.
package velad

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/internal/config"
	"go.xchunk.org/vela/internal/version"
	"go.xchunk.org/vela/internal/wire"
)

// Env abstracts the process environment for tests.
type Env struct {
	Stdout    io.Writer
	Stderr    io.Writer
	LookupEnv func(string) (string, bool)
	// OnListen, if set, is called with the bound address once serve listens.
	OnListen func(net.Addr)
}

// Main runs the command line and returns the process exit code.
func Main(ctx context.Context, args []string) int {
	cmd := NewCommand(Env{Stdout: os.Stdout, Stderr: os.Stderr, LookupEnv: os.LookupEnv})
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "velad:", err)
		return 1
	}
	return 0
}

// NewCommand builds the velad command tree.
func NewCommand(env Env) *cobra.Command {
	root := &cobra.Command{
		Use:           "velad",
		Short:         "Vela file store server",
		Long:          "velad serves the Vela file store. Without a subcommand it runs \"serve\".",
		Version:       version.Get().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
	}
	// The server binary runs in a container; shell completion is noise.
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	config.RegisterFlags(root.PersistentFlags(), false)

	serve := newServeCommand(env)
	root.RunE = serve.RunE
	root.AddCommand(
		serve,
		newKeyCommand(env),
		newSigningCommand(env),
		newJanitorCommand(env),
		newBackupCommand(env),
		newHealthcheckCommand(env),
		newMigrateCommand(env),
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "velad", version.Get().String())
			},
		},
	)
	return root
}

func loadConfig(cmd *cobra.Command, env Env) (*config.Config, error) {
	return config.Load(cmd.Flags(), env.LookupEnv)
}

// adminLogger is used by the maintenance subcommands: their output is for a
// human at a terminal, so only warnings and errors are logged.
func adminLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	c := *cfg
	if c.LogLevel < slog.LevelWarn {
		c.LogLevel = slog.LevelWarn
	}
	return c.Logger(w)
}

// openStorage opens the data directory for a maintenance subcommand.
func openStorage(cmd *cobra.Command, env Env) (*config.Config, *wire.Storage, *slog.Logger, error) {
	cfg, err := loadConfig(cmd, env)
	if err != nil {
		return nil, nil, nil, err
	}
	log := adminLogger(cfg, env.Stderr)
	var st *wire.Storage
	if cmd.Parent() != nil && cmd.Parent().Name() == "janitor" {
		st, err = wire.OpenConfiguredStorage(cmd.Context(), cfg, log)
	} else {
		// Key management and metadata backups must work during an R2 outage.
		st, err = wire.OpenStorage(cmd.Context(), cfg.DataDir, log)
		if err == nil {
			st.Kind = cfg.Storage
		}
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open data directory %s: %w", cfg.DataDir, err)
	}
	return cfg, st, log, nil
}
