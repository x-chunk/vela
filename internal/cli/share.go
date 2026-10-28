package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/output"
)

func (a *App) shareCommand() *cobra.Command {
	var ttl, note string
	var copyURL bool
	var byTag tagFlags
	var conf confirmFlags
	cmd := &cobra.Command{
		Use:   "share PATH [--ttl 2h] [--note TEXT] [--copy]",
		Short: "Create a temporary signed link to a file",
		Long: "Create a signed link that gives access to one file, of any visibility, until it expires.\n" +
			"Links cannot be permanent (use unlisted for that) and survive renames. Revoke them with\n" +
			"\"vela links revoke\".\n\n" +
			"--by-tag and --by-tags create one link per object with these tags below the given paths\n" +
			"(everything by default), after showing them and asking unless -y is given.",
		Example: "  vela share private/report.pdf --ttl 2h --note 'for the reviewer' --copy\n" +
			"  vela share --by-tag review --ttl 7d --dry-run",
		Args:              byTag.args(exactArgs(1)),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ttl != "" {
				if _, err := api.ParseDuration(ttl); err != nil {
					return usageError(err)
				}
			}
			if byTag.used() {
				return a.shareTagged(cmd, args, &byTag, conf, api.CreateLinkRequest{TTL: ttl, Note: note}, copyURL)
			}
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
			l, err := c.CreateLink(ctx, api.CreateLinkRequest{Path: path, TTL: ttl, Note: note})
			if err != nil {
				return a.notFoundHint(ctx, c, err, path)
			}
			if copyURL && l.URL != nil {
				if err := a.copyToClipboard(cmd, *l.URL); err != nil {
					a.printer.Warnf("could not copy to the clipboard: %v", err)
				} else {
					a.printer.Infof("Copied to the clipboard.")
				}
			}
			if a.jsonOut {
				return a.printer.JSONValue(l)
			}
			if l.URL != nil {
				a.printer.Println(*l.URL)
			}
			a.printer.Infof("Expires %s (in %s). Revoke with: vela links revoke %s", output.Time(l.ExpiresAt), a.printer.Remaining(l.ExpiresAt), l.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "lifetime, e.g. 90m, 2h, 7d (default: server default, 24h)")
	cmd.Flags().StringVar(&note, "note", "", "free-text note shown in \"vela links ls\"")
	cmd.Flags().BoolVar(&copyURL, "copy", false, "copy the URL to the clipboard")
	byTag.register(cmd)
	conf.register(cmd, "create the links without asking")
	return cmd
}

// shareTagged creates one link per object of a tag selection, from req
// without its path.
func (a *App) shareTagged(cmd *cobra.Command, scopes []string, byTag *tagFlags, conf confirmFlags, req api.CreateLinkRequest, copyURL bool) error {
	f, err := byTag.filter()
	if err != nil {
		return err
	}
	c, _, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	objs, err := a.selectTagged(ctx, c, scopes, f)
	if err != nil {
		return err
	}
	if len(objs) == 0 {
		return errNothingTagged(f)
	}
	paths := objectPaths(objs)
	ok, err := a.confirmPlan(paths, objs, fmt.Sprintf("Create %d signed link(s)?", len(objs)), conf.yes, conf.dryRun)
	if err != nil || !ok {
		return err
	}
	links := []*api.Link{}
	errs := make([]error, len(objs))
	var rows [][]any
	var urls []string
	for i, o := range objs {
		req.Path = o.Path
		l, err := c.CreateLink(ctx, req)
		if err != nil {
			errs[i] = fmt.Errorf("%s: %w", o.Path, err)
			continue
		}
		links = append(links, l)
		u := ""
		if l.URL != nil {
			u = *l.URL
			urls = append(urls, u)
		}
		rows = append(rows, []any{o.Path, u})
	}
	if !a.jsonOut {
		a.printer.Table(nil, rows)
	}
	if copyURL && len(urls) > 0 {
		if err := a.copyToClipboard(cmd, strings.Join(urls, "\n")); err != nil {
			a.printer.Warnf("could not copy to the clipboard: %v", err)
		} else {
			a.printer.Infof("Copied %d URL(s) to the clipboard.", len(urls))
		}
	}
	if err := batchResult("share", len(objs), errs, paths); err != nil {
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(links)
	}
	if len(links) > 0 {
		a.printer.Infof("Expire %s (in %s). Revoke with: vela links revoke --by-tag ...", output.Time(links[0].ExpiresAt), a.printer.Remaining(links[0].ExpiresAt))
	}
	return nil
}

// copyToClipboard uses whichever clipboard tool exists; there is no portable
// clipboard API without cgo.
func (a *App) copyToClipboard(cmd *cobra.Command, text string) error {
	candidates := [][]string{{"pbcopy"}, {"clip.exe"}}
	if a.env("WAYLAND_DISPLAY") != "" {
		candidates = append([][]string{{"wl-copy"}}, candidates...)
	}
	candidates = append(candidates, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	for _, c := range candidates {
		if _, err := a.LookPath(c[0]); err != nil {
			continue
		}
		return a.Run(cmd.Context(), strings.NewReader(text), false, c[0], c[1:]...)
	}
	return fmt.Errorf("no clipboard tool found (wl-copy, xclip, xsel, pbcopy or clip.exe)")
}

func (a *App) linksCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "links", Short: "List or revoke signed links"}
	var all bool
	ls := &cobra.Command{
		Use:               "ls [PATH]",
		Short:             "List signed links (active ones by default)",
		Args:              rangeArgs(0, 1),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := client.LinkListOptions{IncludeExpired: all, IncludeRevoked: all}
			if len(args) == 1 {
				p, err := remotePath(args[0])
				if err != nil {
					return err
				}
				opts.Path = p
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			list, err := c.ListLinks(ctx, opts)
			if err != nil {
				return err
			}
			return a.printLinks(list)
		},
	}
	ls.Flags().BoolVar(&all, "all", false, "include expired and revoked links")
	revoke := &cobra.Command{
		Use:   "revoke ID...",
		Short: "Revoke signed links; they stop working immediately",
		Args:  minArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			errs := make([]error, len(args))
			for i, id := range args {
				if errs[i] = c.RevokeLink(ctx, strings.ToLower(id)); errs[i] == nil {
					a.printer.Printf("Revoked %s\n", id)
				}
			}
			if err := batchResult("revoke", len(args), errs, args); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(map[string][]string{"revoked": args})
			}
			return nil
		},
	}
	cmd.AddCommand(ls, revoke)
	return cmd
}

func (a *App) printLinks(list *api.LinkList) error {
	if a.jsonOut {
		return a.printer.JSONValue(list)
	}
	if len(list.Links) == 0 {
		a.printer.Infof("No links.")
		return nil
	}
	rows := make([][]any, 0, len(list.Links))
	for _, l := range list.Links {
		status := a.printer.Remaining(l.ExpiresAt)
		if l.RevokedAt != nil {
			status = "revoked"
		}
		var path any = l.Path
		if l.Path == "" {
			path = a.printer.Dim("(in trash or deleted)")
		}
		rows = append(rows, []any{l.ID, status, path, l.Note})
	}
	a.printer.Table([]string{"ID", "EXPIRES IN", "PATH", "NOTE"}, rows)
	return nil
}
