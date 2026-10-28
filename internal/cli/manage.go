package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
)

func (a *App) mvCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "mv FROM TO",
		Short: "Rename or move files and directories",
		Long: "Rename or move. FROM ending with / moves a whole directory in one atomic step. TO ending\n" +
			"with / moves a file into that directory under its own name. Public URLs change; signed\n" +
			"links keep working.",
		Example: "  vela mv notes/a.md notes/b.md\n  vela mv report.pdf archive/\n  vela mv docs/math/ archive/math/",
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, to := strings.TrimLeft(args[0], "/"), strings.TrimLeft(args[1], "/")
			req := api.MoveRequest{Overwrite: force}
			if strings.HasSuffix(from, "/") {
				f, err := remotePrefix(from)
				if err != nil {
					return err
				}
				t, err := remotePrefix(to)
				if err != nil {
					return err
				}
				req.From, req.To, req.Recursive = f, t, true
			} else {
				f, err := remotePath(from)
				if err != nil {
					return err
				}
				if to == "" || strings.HasSuffix(to, "/") {
					to += api.BaseName(f)
				}
				t, err := remotePath(to)
				if err != nil {
					return err
				}
				req.From, req.To = f, t
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			res, err := c.Move(ctx, req)
			if err != nil {
				err = hintFor(err, api.CodeConflict, "use --force to replace destination directory policies")
				err = hintFor(err, api.CodeAlreadyExists, "use --force to overwrite (the overwritten files go to the trash)")
				if !req.Recursive {
					err = a.notFoundHint(ctx, c, err, req.From)
				}
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(res)
			}
			a.printer.Printf("Moved %d object(s): %s -> %s\n", res.Moved, req.From, req.To)
			if res.PoliciesMoved > 0 {
				a.printer.Printf("Moved %d directory policy/policies.\n", res.PoliciesMoved)
			}
			return nil
		},
		ValidArgsFunction: a.completeRemote(),
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing destinations (they go to the trash)")
	return cmd
}

func (a *App) rmCommand() *cobra.Command {
	var recursive, purge, yes, broken bool
	cmd := &cobra.Command{
		Use:   "rm PATH...",
		Short: "Delete files (to the trash)",
		Long: "Move files to the trash, from where \"vela trash restore\" brings them back until they\n" +
			"are purged automatically. Directories (ending with /) need -r. --purge deletes for good\n" +
			"(admin keys only). -r and --purge ask for confirmation; -y skips it, and is required\n" +
			"when stdin is not a terminal. Removing a symlink never touches the file it points at;\n" +
			"removing a file breaks the symlinks pointing at it. --broken removes the broken\n" +
			"symlinks in the given directories (with -r, also below them).",
		Example: "  vela rm notes/old.md\n  vela rm -r drafts/\n  vela rm --purge -y secret.pdf\n  vela rm --broken -r sorted/",
		Args:    minArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if broken {
				return a.rmBroken(cmd, args, recursive, purge, yes)
			}
			paths := make([]string, len(args))
			for i, raw := range args {
				p, isDir, err := api.CleanPath(raw)
				if err != nil {
					return usageError(err)
				}
				if p == "" {
					return usagef("refusing to delete the root directory")
				}
				if isDir && !recursive {
					return usagef("%s is a directory (use -r)", p)
				}
				paths[i] = p
			}
			if recursive || purge {
				what := "Delete"
				if purge {
					what = "Permanently delete"
				}
				q := fmt.Sprintf("%s %s", what, strings.Join(paths, ", "))
				if recursive {
					q += " (recursively)"
				}
				if err := a.confirm(q+"?", yes); err != nil {
					return err
				}
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			errs := make([]error, len(paths))
			var results []*api.DeleteResponse
			for i, p := range paths {
				res, err := c.Delete(ctx, p, client.DeleteOptions{Recursive: recursive, Purge: purge})
				if err != nil {
					if api.IsCode(err, api.CodeInvalidRequest) && !recursive {
						err = withHint(err, "use -r to delete a directory")
					}
					errs[i] = fmt.Errorf("%s: %w", p, err)
					continue
				}
				results = append(results, res)
				switch {
				case purge:
					a.printer.Printf("Deleted %s permanently (%d object(s))\n", p, res.Deleted)
				default:
					a.printer.Printf("Moved %s to the trash (%d object(s))\n", p, res.Deleted)
				}
				a.warnSymlinksAffected(res.SymlinksAffected, purge)
			}
			if err := batchResult("delete", len(paths), errs, paths); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(results)
			}
			if !purge {
				a.printer.Infof("Restore with: vela trash restore <path>")
			}
			return nil
		},
		ValidArgsFunction: a.completeRemote(),
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "delete directories recursively")
	cmd.Flags().BoolVar(&purge, "purge", false, "delete permanently, bypassing the trash (admin)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&broken, "broken", false, "remove the broken symlinks in the given directories")
	return cmd
}

// warnSymlinksAffected reports symlinks a deletion left broken.
func (a *App) warnSymlinksAffected(n int, purged bool) {
	if n == 0 {
		return
	}
	state := "a trashed file"
	if purged {
		state = "a deleted file"
	}
	a.printer.Warnf("%d symlink(s) now point to %s (list them with: vela ls -r --broken)", n, state)
}

// rmBroken removes the broken symlinks in dirs, after one confirmation.
func (a *App) rmBroken(cmd *cobra.Command, dirs []string, recursive, purge, yes bool) error {
	prefixes := make([]string, len(dirs))
	for i, raw := range dirs {
		if raw != "" && !strings.HasSuffix(raw, "/") {
			raw += "/"
		}
		p, err := remotePrefix(raw)
		if err != nil {
			return err
		}
		prefixes[i] = p
	}
	c, _, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	var paths []string
	for _, prefix := range prefixes {
		l, err := c.ListAll(ctx, client.ListOptions{Prefix: prefix, Recursive: recursive, Broken: true, Limit: 1000})
		if err != nil {
			return err
		}
		for _, o := range keepObjects(l.Objects, "", true, false) {
			paths = append(paths, o.Path)
		}
	}
	if len(paths) == 0 {
		if a.jsonOut {
			return a.printer.JSONValue([]*api.DeleteResponse{})
		}
		a.printer.Infof("No broken symlinks found.")
		return nil
	}
	what := "Delete"
	if purge {
		what = "Permanently delete"
	}
	if err := a.confirm(fmt.Sprintf("%s %d broken symlink(s)?", what, len(paths)), yes); err != nil {
		return err
	}
	errs := make([]error, len(paths))
	results := []*api.DeleteResponse{}
	for i, p := range paths {
		res, err := c.Delete(ctx, p, client.DeleteOptions{Purge: purge})
		if err != nil {
			errs[i] = fmt.Errorf("%s: %w", p, err)
			continue
		}
		results = append(results, res)
		a.printer.Printf("Removed %s\n", p)
	}
	if err := batchResult("delete", len(paths), errs, paths); err != nil {
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(results)
	}
	return nil
}

func (a *App) metaCommand() *cobra.Command {
	var (
		title, description, contentType, disposition, expires string
		tags, untags                                          []string
	)
	cmd := &cobra.Command{
		Use:   "meta PATH [flags]",
		Short: "Show or change metadata",
		Long: "Without flags, show the metadata of a file. With flags, change it. The change is applied\n" +
			"only if nobody modified the file in between (optimistic locking on the revision).",
		Example: "  vela meta report.pdf\n  vela meta report.pdf --title 'Q1 report' --tag q1 --untag draft\n" +
			"  vela meta report.pdf --expires 7d\n  vela meta report.pdf --expires none --disposition attachment",
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
			cur, err := c.Stat(ctx, path)
			if err != nil {
				return a.notFoundHint(ctx, c, err, path)
			}
			changed := cmd.Flags().Changed
			var patch api.ObjectPatch
			if changed("title") {
				if err := api.ValidateTitle(title); err != nil {
					return usageError(err)
				}
				patch.Title = api.Some(title)
			}
			if changed("description") {
				if err := api.ValidateDescription(description); err != nil {
					return usageError(err)
				}
				patch.Description = api.Some(description)
			}
			if changed("content-type") {
				patch.ContentType = api.Some(contentType)
			}
			if changed("disposition") {
				d, err := api.ParseDisposition(disposition)
				if err != nil {
					return usageError(err)
				}
				patch.Disposition = api.Some(d)
			}
			if changed("expires") {
				t, err := api.ParseExpiry(expires, a.Now())
				if err != nil {
					return usageError(err)
				}
				if t == nil {
					patch.ExpiresAt = api.Null[time.Time]()
				} else {
					patch.ExpiresAt = api.Some(*t)
				}
			}
			if len(tags) > 0 || len(untags) > 0 {
				add, err := api.NormalizeTags(tags)
				if err != nil {
					return usageError(err)
				}
				remove, err := api.NormalizeTags(untags)
				if err != nil {
					return usageError(err)
				}
				// Add first, then remove, so that --untag wins over --tag.
				next := slices.Clone(cur.Tags)
				for _, t := range add {
					if !slices.Contains(next, t) {
						next = append(next, t)
					}
				}
				next = slices.DeleteFunc(next, func(t string) bool { return slices.Contains(remove, t) })
				patch.Tags = api.Some(next)
			}
			if patch.IsEmpty() {
				if a.jsonOut {
					return a.printer.JSONValue(cur)
				}
				a.printObject(ctx, c, cur)
				return nil
			}
			o, err := c.Patch(ctx, path, patch, cur.Revision)
			if err != nil {
				return hintFor(err, api.CodePreconditionFailed, "the file changed meanwhile; run the command again")
			}
			if a.jsonOut {
				return a.printer.JSONValue(o)
			}
			a.printObject(ctx, c, o)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "set the title (\"\" clears it)")
	f.StringVar(&description, "description", "", "set the description")
	f.StringArrayVar(&tags, "tag", nil, "add a tag (repeatable)")
	f.StringArrayVar(&untags, "untag", nil, "remove a tag (repeatable)")
	f.StringVar(&contentType, "content-type", "", "set the MIME type")
	f.StringVar(&disposition, "disposition", "", "inline or attachment")
	f.StringVar(&expires, "expires", "", "expire after a duration (7d), at an RFC 3339 time, or none")
	return cmd
}

func (a *App) visCommand() *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:   "vis PATH... public|unlisted|private",
		Short: "Change visibility",
		Long: "Change the visibility of files, or with -r of everything below directories (files and\n" +
			"directories may be mixed). A path that matches nothing is an error. The change takes effect\n" +
			"immediately. Unlisted is not access control: anyone with the URL can read it.",
		Example: "  vela vis report.pdf private\n  vela vis -r docs/math/ public",
		Args:    minArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vis, err := api.ParseVisibility(args[len(args)-1])
			if err != nil {
				return usageError(err)
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			// Resolve every argument before changing anything: a typo must
			// not leave the command half done or report a hollow success.
			var targets []api.Object
			for _, raw := range args[:len(args)-1] {
				objs, err := a.resolveTargets(ctx, c, raw, recursive)
				if err != nil {
					return err
				}
				targets = append(targets, objs...)
			}
			targets = uniqueObjects(targets)
			return a.applyVisibility(ctx, c, targets, vis)
		},
		ValidArgsFunction: a.completeRemote(),
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "apply to everything below directories")
	return cmd
}

// resolveTargets turns a vis argument into objects. With recursive, a path
// names a file if one exists there, and otherwise the directory below it;
// a trailing slash always names a directory. Nothing found is not_found.
func (a *App) resolveTargets(ctx context.Context, c *client.Client, raw string, recursive bool) ([]api.Object, error) {
	if !strings.HasSuffix(raw, "/") {
		p, err := remotePath(raw)
		if err != nil {
			return nil, err
		}
		o, err := c.Stat(ctx, p)
		if err == nil {
			return []api.Object{*o}, nil
		}
		if !recursive || !api.IsCode(err, api.CodeNotFound) {
			return nil, a.notFoundHint(ctx, c, err, p)
		}
	} else if !recursive {
		return nil, usagef("%s is a directory (use -r)", raw)
	}
	prefix, err := remotePrefix(raw)
	if err != nil {
		return nil, err
	}
	l, err := c.ListAll(ctx, client.ListOptions{Prefix: prefix, Recursive: true, Limit: 1000})
	if err != nil {
		return nil, err
	}
	if len(l.Objects) == 0 {
		return nil, &cliError{code: exitNotFound, slug: string(api.CodeNotFound), msg: "nothing at " + raw}
	}
	return l.Objects, nil
}

// uniqueObjects drops repeated objects (the same file given twice, or
// nested directories): patching one twice with the same expected revision
// would fail the second time.
func uniqueObjects(objs []api.Object) []api.Object {
	seen := make(map[string]bool, len(objs))
	out := objs[:0]
	for _, o := range objs {
		if !seen[o.ID] {
			seen[o.ID] = true
			out = append(out, o)
		}
	}
	return out
}

func (a *App) applyVisibility(ctx context.Context, c *client.Client, targets []api.Object, vis api.Visibility) error {
	updated, changed, err := applyPatches(ctx, c, "visibility change", targets, func(o api.Object) api.ObjectPatch {
		if o.Visibility == vis {
			return api.ObjectPatch{}
		}
		return api.ObjectPatch{Visibility: api.Some(vis)}
	})
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(updated)
	}
	a.printer.Printf("%d object(s) now %s (%d changed)\n", len(targets), vis, changed)
	return nil
}

// applyPatches patches targets concurrently, each against the revision it
// was resolved at. patchFor returns an empty patch for objects that already
// match, which are then left alone and returned as they are.
func applyPatches(ctx context.Context, c *client.Client, op string, targets []api.Object,
	patchFor func(api.Object) api.ObjectPatch) ([]api.Object, int, error) {
	errs := make([]error, len(targets))
	paths := make([]string, len(targets))
	updated := make([]*api.Object, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	changed := 0
	for i := range targets {
		paths[i] = targets[i].Path
		patch := patchFor(targets[i])
		if patch.IsEmpty() {
			o := targets[i]
			updated[i] = &o
			continue
		}
		changed++
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			updated[i], errs[i] = c.Patch(ctx, targets[i].Path, patch, targets[i].Revision)
		}(i)
	}
	wg.Wait()
	if err := batchResult(op, len(targets), errs, paths); err != nil {
		return nil, 0, err
	}
	out := make([]api.Object, 0, len(updated))
	for _, o := range updated {
		out = append(out, *o)
	}
	return out, changed, nil
}
