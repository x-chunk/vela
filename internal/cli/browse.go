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
	var recursive, long, broken bool
	var visibility []string
	var kind, target string
	var byTag tagFlags
	cmd := &cobra.Command{
		Use:   "ls [DIR/]",
		Short: "List a directory",
		Long: "List a directory (the root by default). With -r, list every object below it.\n" +
			"Naming a file instead of a directory shows that file. --by-tag and --by-tags search\n" +
			"everything below the directory (as -r does) for objects with these tags.",
		Example: "  vela ls\n  vela ls docs/math/ -l\n  vela ls -r docs/ --visibility private\n" +
			"  vela ls --by-tag q1 --by-tags 'math draft'",
		Args: rangeArgs(0, 1),
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
			var k api.ObjectKind
			if kind != "" {
				if k, err = api.ParseObjectKind(kind); err != nil {
					return usageError(err)
				}
			}
			if target != "" {
				if target, err = remotePath(target); err != nil {
					return err
				}
			}
			tags, err := byTag.filter()
			if err != nil {
				return err
			}
			if len(tags) > 0 {
				recursive = true
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			l, err := c.ListAll(ctx, client.ListOptions{
				Prefix: prefix, Recursive: recursive, Visibility: vis, Kind: k, Broken: broken, Target: target, Tags: tags, Limit: 1000,
			})
			if err != nil {
				return err
			}
			if k != "" || broken || target != "" {
				l.Objects = keepObjects(l.Objects, k, broken, target != "")
			}
			// "vela ls docs/a.pdf" names a file: show it rather than an
			// empty directory.
			if len(l.Dirs) == 0 && len(l.Objects) == 0 && raw != "" && !strings.HasSuffix(raw, "/") {
				if o, err := c.Stat(ctx, raw); err == nil && tags.Match(o.Tags) {
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
				// The collection exists even without any rules. Its 404 means
				// an older API v1 server has no directory-policy endpoint.
				if err != nil && !api.IsCode(err, api.CodeNotFound) {
					return err
				}
				if err == nil {
					for _, rule := range rules.Policies {
						policies[rule.Prefix] = rule
					}
				}
			}
			a.printListing(l, long, policies)
			return nil
		},
		ValidArgsFunction: a.completeRemote(),
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "list all objects below the directory")
	cmd.Flags().BoolVarP(&long, "long", "l", false, "long format: visibility, size, date, title")
	cmd.Flags().StringSliceVar(&visibility, "visibility", nil, "only show these effective visibilities (public,unlisted,private)")
	cmd.Flags().StringVar(&kind, "type", "", "only show files or symlinks (file, symlink)")
	cmd.Flags().BoolVar(&broken, "broken", false, "only show broken symlinks")
	cmd.Flags().StringVar(&target, "target", "", "only show symlinks to this file")
	byTag.register(cmd)
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
		rows = append(rows, []any{"d", "", "-", "-", output.Styled(string(p.Dir(rel(d))) + labels), ""})
	}
	for i := range l.Objects {
		o := &l.Objects[i]
		name := output.Clean(rel(o.Path))
		if isSymlink(o) {
			name += " -> " + output.Clean(targetLabel(o))
		}
		if o.Expired {
			name += " " + string(p.Dim("(expired)"))
		}
		if isBroken(o) {
			name += " " + string(p.Error(brokenLabel(o)))
		}
		kind := "-"
		if isSymlink(o) {
			kind = "l"
		}
		rows = append(rows, []any{kind, p.Visibility(o.Visibility), output.Size(o.Size), output.Time(o.UpdatedAt), output.Styled(name), o.Title})
	}
	p.Table(nil, rows)
}

func (a *App) statCommand() *cobra.Command {
	var byTag tagFlags
	cmd := &cobra.Command{
		Use:   "stat PATH",
		Short: "Show all metadata of an object",
		Long: "Show all metadata of an object. With --by-tag or --by-tags, show every object with\n" +
			"these tags below the given paths (everything by default); --json prints an array.",
		Example:           "  vela stat docs/a.pdf\n  vela stat --by-tag q1 docs/",
		Args:              byTag.args(exactArgs(1)),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if byTag.used() {
				return a.statTagged(cmd, args, &byTag)
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
			o, err := c.Stat(ctx, path)
			if err != nil {
				return a.notFoundHint(ctx, c, err, path)
			}
			if a.jsonOut {
				return a.printer.JSONValue(o)
			}
			a.printObject(ctx, c, o)
			return nil
		},
	}
	byTag.register(cmd)
	return cmd
}

func (a *App) statTagged(cmd *cobra.Command, scopes []string, byTag *tagFlags) error {
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
	if a.jsonOut {
		return a.printer.JSONValue(objs)
	}
	for i := range objs {
		if i > 0 {
			a.printer.Println()
		}
		a.printObject(ctx, c, &objs[i])
	}
	return nil
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

// printObject prints every metadata field of o and, best effort, how it
// relates to symlinks: a symlink's target, or the symlinks pointing at a
// file.
func (a *App) printObject(ctx context.Context, c *client.Client, o *api.Object) {
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
	// A failed lookup only leaves the symlink list out of the report.
	links, _ := symlinksTo(ctx, c, o)
	p.KeyValues(a.symlinkDetails(o, links))
	if isBroken(o) {
		p.Warnf("%s is a broken symlink; %s", o.Path, brokenHint(o))
	}
}

func (a *App) urlCommand() *cobra.Command {
	var byTag tagFlags
	cmd := &cobra.Command{
		Use:   "url PATH",
		Short: "Print the public URL of an object or directory index",
		Long: "Print the public URL of an object or directory index. With --by-tag or --by-tags,\n" +
			"print the URLs of every object with these tags below the given paths (everything by\n" +
			"default), one per line; private objects have none and are reported on stderr.",
		Example:           "  vela url docs/a.pdf\n  vela url docs/\n  vela url --by-tag handout",
		Args:              byTag.args(exactArgs(1)),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if byTag.used() {
				urls, err := a.taggedURLs(cmd, args, &byTag)
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.printer.JSONValue(urls)
				}
				for _, u := range urls {
					a.printer.Println(u.URL)
				}
				return nil
			}
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
	byTag.register(cmd)
	return cmd
}

// taggedURL is one entry of "vela url --by-tag --json".
type taggedURL struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

// taggedURLs returns the public URLs of a tag selection. Private objects are
// skipped with a warning; a selection without any public URL is an error.
func (a *App) taggedURLs(cmd *cobra.Command, scopes []string, byTag *tagFlags) ([]taggedURL, error) {
	f, err := byTag.filter()
	if err != nil {
		return nil, err
	}
	c, _, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	objs, err := a.selectTagged(ctx, c, scopes, f)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, errNothingTagged(f)
	}
	urls := []taggedURL{}
	for _, o := range objs {
		if o.URL == nil {
			a.printer.Warnf("%s is private and has no public URL (vela share %s)", o.Path, o.Path)
			continue
		}
		urls = append(urls, taggedURL{Path: o.Path, URL: *o.URL})
	}
	if len(urls) == 0 {
		return nil, &cliError{code: exitError, slug: "private", msg: "every object tagged " + f.String() + " is private",
			hint: "create temporary links with: vela share --by-tag ..."}
	}
	return urls, nil
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
			if !api.IsCode(err, api.CodeNotFound) {
				return "", err
			}
			// An individual 404 is not enough to infer missing support. Probe the
			// collection, which never returns not_found on a supporting server.
			_, probeErr := c.ListDirPolicies(ctx, client.DirListOptions{Limit: 1})
			if probeErr == nil {
				return "", err
			}
			if !api.IsCode(probeErr, api.CodeNotFound) {
				return "", probeErr
			}
			info, err := c.Info(ctx)
			if err != nil {
				return "", err
			}
			return api.PublicURL(info.PublicURL, prefix), nil
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
	var byTag tagFlags
	var conf confirmFlags
	cmd := &cobra.Command{
		Use:   "open PATH",
		Short: "Open the public URL of an object or directory in a browser",
		Long: "Open the public URL of an object or directory index in a browser. With --by-tag or\n" +
			"--by-tags, open one tab per object with these tags below the given paths (everything\n" +
			"by default); private objects are skipped. It shows the URLs and asks before opening\n" +
			"them unless -y is given.",
		Example:           "  vela open docs/a.pdf\n  vela open --by-tag slides --dry-run",
		Args:              byTag.args(exactArgs(1)),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if byTag.used() {
				return a.openTagged(cmd, args, &byTag, conf)
			}
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
	byTag.register(cmd)
	conf.register(cmd, "open the tabs without asking")
	return cmd
}

func (a *App) openTagged(cmd *cobra.Command, scopes []string, byTag *tagFlags, conf confirmFlags) error {
	urls, err := a.taggedURLs(cmd, scopes, byTag)
	if err != nil {
		return err
	}
	lines := make([]string, len(urls))
	for i, u := range urls {
		lines[i] = u.URL
	}
	ok, err := a.confirmPlan(lines, urls, fmt.Sprintf("Open %d browser tab(s)?", len(urls)), conf.yes, conf.dryRun)
	if err != nil || !ok {
		return err
	}
	for _, u := range urls {
		name, openArgs := browserCommand(u.URL)
		if err := a.Run(cmd.Context(), nil, false, name, openArgs...); err != nil {
			return fmt.Errorf("open a browser with %s: %w (the URL is %s)", name, err, u.URL)
		}
		a.printer.Infof("Opened %s", u.URL)
	}
	if a.jsonOut {
		return a.printer.JSONValue(urls)
	}
	return nil
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
