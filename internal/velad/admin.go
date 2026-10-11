package velad

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/clock"
	"go.xchunk.org/vela/internal/service"
	"go.xchunk.org/vela/internal/wire"
)

// adminService builds a service for maintenance commands. Options that only
// matter for serving requests are irrelevant here and get harmless values.
func adminService(cmd *cobra.Command, env Env) (*service.Service, *wire.Storage, error) {
	cfg, st, log, err := openStorage(cmd, env)
	if err != nil {
		return nil, nil, err
	}
	svc, err := wire.NewService(st, clock.System{}, log, service.Options{
		PublicURL:            cfg.PublicURL,
		MaxUpload:            cfg.MaxUpload,
		MaxConcurrentUploads: cfg.MaxConcurrentUploads,
		MaxCreate:            cfg.MaxCreate,
		LinkDefaultTTL:       cfg.LinkDefaultTTL,
		LinkMaxTTL:           cfg.LinkMaxTTL,
		LinkRetention:        cfg.LinkRetention,
		TrashRetention:       cfg.TrashRetention,
		DefaultVisibility:    cfg.DefaultVisibility,
	})
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return svc, st, nil
}

func newKeyCommand(env Env) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage API keys"}

	var name, scope, prefix string
	create := &cobra.Command{
		Use:   "create --name NAME --scope ro|rw|admin [--prefix DIR/]",
		Short: "Create an API key and print it once",
		Long: "Create an API key. The full key is printed to stdout exactly once; the server only\n" +
			"stores a hash. Details go to stderr, so KEY=$(velad key create ...) captures only the key.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sc, err := api.ParseScope(scope)
			if err != nil {
				return err
			}
			svc, st, err := adminService(cmd, env)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			k, err := svc.CreateKey(cmd.Context(), name, sc, prefix)
			if err != nil {
				return err
			}
			p := k.Key.Prefix
			if p == "" {
				p = "(none)"
			}
			_, _ = fmt.Fprintf(env.Stderr, "Created key %q: id %s, scope %s, prefix %s.\n", k.Key.Name, k.Key.ID, k.Key.Scope, p)
			_, _ = fmt.Fprintln(env.Stderr, "Store it now; it cannot be shown again.")
			_, _ = fmt.Fprintln(env.Stdout, k.Full)
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "human-readable key name (required)")
	create.Flags().StringVar(&scope, "scope", "", "ro, rw or admin (required)")
	create.Flags().StringVar(&prefix, "prefix", "", "restrict the key to this directory, e.g. docs/")
	_ = create.MarkFlagRequired("name")
	_ = create.MarkFlagRequired("scope")

	list := &cobra.Command{
		Use:   "list",
		Short: "List API keys (without secrets)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, st, err := adminService(cmd, env)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			keys, err := svc.ListKeys(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "ID\tNAME\tSCOPE\tPREFIX\tCREATED\tLAST USED\tREVOKED")
			for _, k := range keys {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Scope, dash(k.Prefix),
					fmtTime(&k.CreatedAt), fmtTime(k.LastUsedAt), fmtTime(k.RevokedAt))
			}
			return tw.Flush()
		},
	}

	revoke := &cobra.Command{
		Use:   "revoke KEY_ID...",
		Short: "Revoke API keys; requests with them fail immediately",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, st, err := adminService(cmd, env)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			var errs []error
			for _, id := range args {
				if err := svc.RevokeKey(cmd.Context(), id); err != nil {
					errs = append(errs, err)
					continue
				}
				_, _ = fmt.Fprintf(env.Stdout, "Revoked %s\n", id)
			}
			return errors.Join(errs...)
		},
	}
	cmd.AddCommand(create, list, revoke)
	return cmd
}

func newSigningCommand(env Env) *cobra.Command {
	cmd := &cobra.Command{Use: "signing", Short: "Manage the keys that sign links"}
	var revokeAll bool
	rotate := &cobra.Command{
		Use:   "rotate [--revoke-all]",
		Short: "Start signing new links with a fresh key",
		Long: "Activate a new signing key. The previous key is retired: it keeps verifying the links it\n" +
			"signed until they expire. With --revoke-all every older key is destroyed instead and all\n" +
			"existing signed links stop working immediately.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, st, err := adminService(cmd, env)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			res, err := svc.RotateSigningKey(cmd.Context(), revokeAll)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(env.Stdout, "New signing key: %s\n", res.NewKeyID)
			if revokeAll {
				_, _ = fmt.Fprintf(env.Stdout, "Destroyed %d old key(s); revoked %d link(s).\n", len(res.DeletedKeys), res.RevokedLinks)
			} else {
				_, _ = fmt.Fprintf(env.Stdout, "Retired %d key(s); their links keep working until they expire.\n", len(res.RetiredKeys))
			}
			return nil
		},
	}
	rotate.Flags().BoolVar(&revokeAll, "revoke-all", false, "destroy old keys and invalidate every existing link")
	cmd.AddCommand(rotate)
	return cmd
}

func newJanitorCommand(env Env) *cobra.Command {
	cmd := &cobra.Command{Use: "janitor", Short: "Run background cleanup by hand"}
	run := &cobra.Command{
		Use:   "run",
		Short: "Run one janitor pass now",
		Long:  "Run one cleanup pass. Safe while the server runs: passes never overlap across processes.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, st, err := adminService(cmd, env)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			report, err := svc.RunJanitor(cmd.Context())
			if err != nil {
				return err
			}
			if report.Skipped {
				return errors.New("another janitor pass is running; try again later")
			}
			tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "TASK\tCOUNT\tDURATION\tERROR")
			for _, t := range report.Tasks {
				msg := "-"
				if t.Err != nil {
					msg = t.Err.Error()
				}
				_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", t.Name, t.Count, t.Duration.Round(time.Millisecond), msg)
			}
			_ = tw.Flush()
			return report.Err()
		},
	}
	cmd.AddCommand(run)
	return cmd
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}
