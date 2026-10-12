package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/output"
)

// remotePath validates an object path given on the command line, so that a
// typo fails locally with exit 2 instead of a round trip.
func remotePath(raw string) (string, error) {
	p, err := api.CleanObjectPath(raw)
	if err != nil {
		return "", usageError(err)
	}
	return p, nil
}

func remotePrefix(raw string) (string, error) {
	p, err := api.CleanPrefix(raw)
	if err != nil {
		return "", usageError(err)
	}
	return p, nil
}

func (a *App) lsCommand() *cobra.Command {
	var recursive, long bool
	var visibility []string
	cmd := &cobra.Command{
		Use:   "ls [DIR/]",
		Short: "List a directory",
		Long: "List a directory (the root by default). With -r, list every object below it.\n" +
			"Naming a file instead of a directory shows that file.",
		Example: "  vela ls\n  vela ls docs/math/ -l\n  vela ls -r docs/ --visibility private",
		Args:    rangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := ""
			if len(args) == 1 {
				raw = args[0]
			}
			prefix, err := remotePrefix(raw)
			if err != nil {
				return err
			}
			var vis []api.Visibility
			for _, v := range visibility {
				for _, part := range strings.Split(v, ",") {
					pv, err := api.ParseVisibility(part)
					if err != nil {
						return usageError(err)
					}
					vis = append(vis, pv)
				}
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			l, err := c.ListAll(ctx, client.ListOptions{Prefix: prefix, Recursive: recursive, Visibility: vis, Limit: 1000})
			if err != nil {
				return err
			}
			// "vela ls docs/a.pdf" names a file: show it rather than an
			// empty directory.
			if len(l.Dirs) == 0 && len(l.Objects) == 0 && raw != "" && !strings.HasSuffix(raw, "/") {
				if o, err := c.Stat(ctx, raw); err == nil {
					l.Prefix = api.ParentPrefix(o.Path)
					l.Objects = []api.Object{*o}
				}
			}
			if a.jsonOut {
				return a.printer.JSONValue(l)
			}
			policies := map[string]api.DirPolicy{}
			if long && len(l.Dirs) > 0 {
				rules, err := c.ListAllDirPolicies(ctx, client.DirListOptions{Prefix: prefix, Recursive: false, Limit: 1000})
				if err != nil {
					return err
				}
				for _, rule := range rules.Policies {
					policies[rule.Prefix] = rule
				}
			}
			a.printListing(l, long, policies)
			return nil
		},
		ValidArgsFunction: a.completeRemote(),
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "list all objects below the directory")
	cmd.Flags().BoolVarP(&long, "long", "l", false, "long format: visibility, size, date, title")
	cmd.Flags().StringSliceVar(&visibility, "visibility", nil, "only show these visibilities (public,unlisted,private)")
	return cmd
}

func (a *App) printListing(l *api.Listing, long bool, policies map[string]api.DirPolicy) {
	p := a.printer
	rel := func(path string) string { return strings.TrimPrefix(path, l.Prefix) }
	if !long {
		for _, d := range l.Dirs {
			p.Println(p.Dir(rel(d)))
		}
		for _, o := range l.Objects {
			p.Println(rel(o.Path))
		}
		return
	}
	var rows [][]any
	for _, d := range l.Dirs {
		labels := ""
		if rule, ok := policies[d]; ok {
			if rule.Hidden {
				labels += " [hidden]"
			}
			if rule.Index == api.IndexOff {
				labels += " [no index]"
			}
		}
		rows = append(rows, []any{"", "-", "-", output.Styled(string(p.Dir(rel(d))) + labels), ""})
	}
	for _, o := range l.Objects {
		var name any = rel(o.Path)
		if o.Expired {
			name = output.Styled(output.Clean(rel(o.Path)) + " " + string(p.Dim("(expired)")))
		}
		rows = append(rows, []any{p.Visibility(o.Visibility), output.Size(o.Size), output.Time(o.UpdatedAt), name, o.Title})
	}
	p.Table(nil, rows)
}

func (a *App) statCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "stat PATH",
		Short:             "Show all metadata of an object",
		Args:              exactArgs(1),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := remotePath(args[0])
			if err != nil {
				return err
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			o, err := c.Stat(ctx, path)
			if err != nil {
				return a.notFoundHint(ctx, c, err, path)
			}
			if a.jsonOut {
				return a.printer.JSONValue(o)
			}
			a.printObject(o)
			return nil
		},
	}
}

// notFoundHint turns "not found" for a directory into a hint about -r or a
// trailing slash.
func (a *App) notFoundHint(ctx context.Context, c *client.Client, err error, path string) error {
	if !api.IsCode(err, api.CodeNotFound) {
		return err
	}
	if l, lerr := c.List(ctx, client.ListOptions{Prefix: path + "/", Limit: 1}); lerr == nil && (len(l.Dirs) > 0 || len(l.Objects) > 0) {
		return withHint(err, fmt.Sprintf("%s/ is a directory; use \"vela ls %s/\"", path, path))
	}
	return err
}

func (a *App) printObject(o *api.Object) {
	p := a.printer
	url := "none (private; create a temporary link with: vela share " + o.Path + ")"
	if o.URL != nil {
		url = *o.URL
	}
	expires := "never"
	if o.ExpiresAt != nil {
		expires = output.Time(*o.ExpiresAt)
		if o.Expired {
			expires += " (expired; moves to the trash on the next cleanup)"
		}
	}
	p.KeyValues([]output.KV{
		{Key: "Path", Value: o.Path},
		{Key: "URL", Value: url},
		{Key: "Visibility", Value: output.Styled(strings.TrimSpace(string(p.Visibility(o.Visibility))))},
		{Key: "Size", Value: fmt.Sprintf("%s (%d bytes)", output.Size(o.Size), o.Size)},
		{Key: "Type", Value: o.ContentType},
		{Key: "Disposition", Value: string(o.Disposition)},
		{Key: "Title", Value: dashIfEmpty(o.Title)},
		{Key: "Description", Value: dashIfEmpty(o.Description)},
		{Key: "Tags", Value: dashIfEmpty(strings.Join(o.Tags, ", "))},
		{Key: "Created", Value: output.Time(o.CreatedAt)},
		{Key: "Updated", Value: output.Time(o.UpdatedAt)},
		{Key: "Expires", Value: expires},
		{Key: "Revision", Value: fmt.Sprint(o.Revision)},
		{Key: "SHA-256", Value: o.SHA256},
		{Key: "ID", Value: o.ID},
	})
}

func (a *App) urlCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "url PATH",
		Short:             "Print the public URL of an object or directory index",
		Args:              exactArgs(1),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := a.publicURL(cmd, args[0])
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(map[string]string{"url": u})
			}
			a.printer.Println(u)
			return nil
		},
	}
}

// publicURL resolves the public URL of an object, or of a directory index
// for paths ending in "/".
func (a *App) publicURL(cmd *cobra.Command, raw string) (string, error) {
	c, _, err := a.client()
	if err != nil {
		return "", err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	if strings.HasSuffix(raw, "/") || raw == "" {
		prefix, err := remotePrefix(raw)
		if err != nil {
			return "", err
		}
		dir, err := c.ShowDir(ctx, prefix)
		if err != nil {
			return "", err
		}
		if dir.URL == nil {
			reason := "directory index is disabled"
			if dir.Effective.Index == api.IndexOn {
				reason = "directory has no live public objects"
			}
			return "", &cliError{code: exitNotFound, slug: "not_found", msg: reason + " (404)", hint: "public files remain accessible through their direct URLs"}
		}
		return *dir.URL, nil
	}
	path, err := remotePath(raw)
	if err != nil {
		return "", err
	}
	o, err := c.Stat(ctx, path)
	if err != nil {
		return "", a.notFoundHint(ctx, c, err, path)
	}
	if o.URL == nil {
		return "", &cliError{code: exitError, slug: "private", msg: path + " is private and has no public URL",
			hint: "create a temporary link with: vela share " + path}
	}
	return *o.URL, nil
}

func (a *App) openCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "open PATH",
		Short:             "Open the public URL of an object or directory in a browser",
		Args:              exactArgs(1),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := a.publicURL(cmd, args[0])
			if err != nil {
				return err
			}
			name, openArgs := browserCommand(u)
			if err := a.Run(cmd.Context(), nil, false, name, openArgs...); err != nil {
				return fmt.Errorf("open a browser with %s: %w (the URL is %s)", name, err, u)
			}
			if a.jsonOut {
				return a.printer.JSONValue(map[string]any{"url": u, "opened": true})
			}
			a.printer.Infof("Opened %s", u)
			return nil
		},
	}
}

func browserCommand(u string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{u}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", u}
	default:
		return "xdg-open", []string{u}
	}
}
