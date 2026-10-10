package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/config"
	"go.xchunk.org/vela/internal/keys"
	"go.xchunk.org/vela/internal/version"
)

func (a *App) loginCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Save a server URL and API key in a profile",
		Long: "Ask for the server URL and an API key (input hidden), verify them against the server\n" +
			"and save them in the config profile (default \"main\"; choose with --profile).\n\n" +
			"Without a terminal, the key is read from the first line of stdin and the URL from --url,\n" +
			"so it can be scripted: echo \"$KEY\" | vela login --url https://store.xchunk.org",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			name := a.flagProfile
			if name == "" {
				name = a.env("VELA_PROFILE")
			}
			if name == "" {
				name = f.DefaultProfile
			}
			if name == "" {
				name = config.DefaultProfileName
			}
			prev := f.Profiles[name]

			url, key := a.flagURL, ""
			interactive := a.InIsTerminal()
			if url == "" {
				url = config.DefaultURL
				if prev != nil && prev.URL != "" {
					url = prev.URL
				}
				if interactive {
					_, _ = fmt.Fprintf(a.Err, "Server URL [%s]: ", url)
					line, err := a.readLine()
					if err != nil {
						return err
					}
					if line = strings.TrimSpace(line); line != "" {
						url = line
					}
				}
			}
			if interactive {
				_, _ = fmt.Fprint(a.Err, "API key: ")
				key, err = a.ReadSecret()
				_, _ = fmt.Fprintln(a.Err)
			} else {
				key, err = a.readLine()
			}
			if err != nil {
				return fmt.Errorf("read the key: %w", err)
			}
			key = strings.TrimSpace(key)
			if _, _, err := keys.Parse(key); err != nil {
				return usagef("that does not look like a Vela API key (vela_<id>_<secret>)")
			}

			c, err := a.newClient(url, key)
			if err != nil {
				return usageError(err)
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			info, err := c.CheckServer(ctx)
			if err != nil {
				return err
			}
			p := &config.Profile{URL: c.BaseURL(), Key: key}
			if prev != nil {
				p.DefaultVisibility = prev.DefaultVisibility
			}
			f.Profiles[name] = p
			if f.DefaultProfile == "" {
				f.DefaultProfile = name
			}
			if err := f.Save(path); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(info)
			}
			a.printer.Printf("Logged in to %s as %q (scope %s%s).\n", info.PublicURL, info.Key.Name, info.Key.Scope, prefixNote(info.Key.Prefix))
			a.printer.Infof("Profile %q saved to %s", name, path)
			return nil
		},
	}
}

func prefixNote(prefix string) string {
	if prefix == "" {
		return ""
	}
	return ", limited to " + prefix
}

func (a *App) logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove a profile and its key from the config",
		Args:  noArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			f, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			s, err := config.Resolve(f, a.flagProfile, "", a.LookupEnv)
			if err != nil {
				return err
			}
			if _, ok := f.Profiles[s.Profile]; !ok {
				return &cliError{code: exitNotFound, slug: "not_found", msg: fmt.Sprintf("no profile %q in %s", s.Profile, path)}
			}
			delete(f.Profiles, s.Profile)
			if f.DefaultProfile == s.Profile {
				f.DefaultProfile = ""
				if names := f.ProfileNames(); len(names) > 0 {
					f.DefaultProfile = names[0]
				}
			}
			if err := f.Save(path); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(map[string]string{"removed_profile": s.Profile})
			}
			a.printer.Printf("Removed profile %q.\n", s.Profile)
			a.printer.Infof("The key itself still works until it is revoked on the server (velad key revoke).")
			return nil
		},
	}
}

func (a *App) whoamiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the key, its scope and prefix, and the server",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, s, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			info, err := c.CheckServer(ctx)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(info)
			}
			pairs := [][2]string{
				{"Key", fmt.Sprintf("%s (%s)", info.Key.Name, info.Key.ID)},
				{"Scope", string(info.Key.Scope)},
				{"Prefix", dashIfEmpty(info.Key.Prefix)},
				{"Server", fmt.Sprintf("%s (velad %s)", info.PublicURL, info.ServerVersion)},
				{"Profile", s.Profile},
			}
			if a.verbose {
				pairs = append(pairs, [2]string{"API key", keys.Mask(s.Key)})
			}
			a.printer.KeyValues(pairs)
			return nil
		},
	}
}

func (a *App) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI and server versions",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := version.Get()
			type serverInfo struct {
				Version    string `json:"version"`
				APIVersion int    `json:"api_version"`
				URL        string `json:"url"`
			}
			var srv *serverInfo
			var serverErr error
			if c, _, err := a.client(); err == nil {
				ctx, cancel := a.ctx(cmd)
				defer cancel()
				info, err := c.Info(ctx)
				if err == nil {
					srv = &serverInfo{Version: info.ServerVersion, APIVersion: info.APIVersion, URL: info.PublicURL}
				}
				serverErr = err
			}
			compatible := srv == nil || srv.APIVersion == api.APIVersion
			if a.jsonOut {
				return a.printer.JSONValue(map[string]any{
					"client":     map[string]any{"version": cli.Version, "commit": cli.Commit, "date": cli.Date, "api_version": api.APIVersion, "go": cli.GoVersion, "platform": cli.Platform},
					"server":     srv,
					"compatible": compatible,
				})
			}
			a.printer.Printf("vela %s (API v%d)\n", cli, api.APIVersion)
			switch {
			case srv != nil:
				a.printer.Printf("server %s at %s (API v%d)\n", srv.Version, srv.URL, srv.APIVersion)
			case serverErr != nil:
				a.printer.Infof("server: unavailable (%v)", serverErr)
			}
			if !compatible {
				return fmt.Errorf("%w: the server speaks API v%d, this CLI v%d; install a matching vela",
					client.ErrIncompatibleAPI, srv.APIVersion, api.APIVersion)
			}
			return nil
		},
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Argument validators that classify mistakes as usage errors (exit 2).

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("%s takes no arguments", cmd.CommandPath())
	}
	return nil
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usagef("%s needs exactly %d argument(s), got %d", cmd.CommandPath(), n, len(args))
		}
		return nil
	}
}

func minArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return usagef("%s needs at least %d argument(s)", cmd.CommandPath(), n)
		}
		return nil
	}
}

func rangeArgs(lo, hi int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < lo || len(args) > hi {
			return usagef("%s needs %d to %d argument(s), got %d", cmd.CommandPath(), lo, hi, len(args))
		}
		return nil
	}
}

var errNothingToDo = errors.New("nothing to do")
