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
	var force, keepTree bool
	var byTag tagFlags
	var conf confirmFlags
	cmd := &cobra.Command{
		Use:   "mv FROM TO",
		Short: "Rename or move files and directories",
		Long: "Rename or move. FROM ending with / moves a whole directory in one atomic step. TO ending\n" +
			"with / moves a file into that directory under its own name. Public URLs change; signed\n" +
			"links keep working.\n\n" +
			"With --by-tag or --by-tags, \"mv [FROM...] TO/\" moves every object with these tags below\n" +
			"FROM (everything by default) into the directory TO: each under its own name, or with\n" +
			"--save-structure under its path relative to FROM. Every conflict (two objects with one\n" +
			"destination, an existing destination without --force) is reported before anything\n" +
			"moves. The moves are shown and need a confirmation unless -y is given.",
		Example: "  vela mv notes/a.md notes/b.md\n  vela mv report.pdf archive/\n  vela mv docs/math/ archive/math/\n" +
			"  vela mv --by-tag q1 docs/ archive/q1/ --save-structure --dry-run",
		Args: byTag.args(exactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if byTag.used() {
				if len(args) == 0 {
					return usagef("%s --by-tag needs a destination directory", cmd.CommandPath())
				}
				return a.mvTagged(cmd, args[:len(args)-1], args[len(args)-1], &byTag, mvTaggedOptions{
					force: force, keepTree: keepTree, confirm: conf,
				})
			}
			if keepTree || conf.dryRun || conf.yes {
				return usagef("--save-structure, --dry-run and -y need --by-tag or --by-tags")
			}
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
	cmd.Flags().BoolVar(&keepTree, "save-structure", false, "with --by-tag, keep paths relative to FROM instead of moving flat")
	byTag.register(cmd)
	conf.register(cmd, "move without asking")
	return cmd
}

type mvTaggedOptions struct {
	force, keepTree bool
	confirm         confirmFlags
}

// taggedMove is one planned move of "vela mv --by-tag".
type taggedMove struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// mvTagged moves the objects of a tag selection into the directory dest. It
// plans every move and checks every destination before the first one, so
// that a conflict never leaves the selection half moved.
func (a *App) mvTagged(cmd *cobra.Command, scopes []string, dest string, byTag *tagFlags, opts mvTaggedOptions) error {
	f, err := byTag.filter()
	if err != nil {
		return err
	}
	dest = strings.TrimLeft(dest, "/")
	if dest != "" && !strings.HasSuffix(dest, "/") {
		dest += "/"
	}
	if dest, err = remotePrefix(dest); err != nil {
		return err
	}
	c, _, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	moves, err := a.planTaggedMoves(ctx, c, scopes, dest, f, opts.keepTree)
	if err != nil {
		return err
	}
	if len(moves) == 0 {
		a.printer.Infof("Every object tagged %s is already in place.", f.String())
		if a.jsonOut {
			return a.printer.JSONValue([]taggedMove{})
		}
		return nil
	}
	if err := checkTaggedMoves(ctx, c, moves, opts.force); err != nil {
		return err
	}
	lines := make([]string, len(moves))
	for i, m := range moves {
		lines[i] = m.From + " -> " + m.To
	}
	if ok, err := a.confirmPlan(lines, moves, fmt.Sprintf("Move %d object(s)?", len(moves)), opts.confirm.yes, opts.confirm.dryRun); err != nil || !ok {
		return err
	}
	errs := make([]error, len(moves))
	paths := make([]string, len(moves))
	done := []taggedMove{}
	for i, m := range moves {
		paths[i] = m.From
		if _, err := c.Move(ctx, api.MoveRequest{From: m.From, To: m.To, Overwrite: opts.force}); err != nil {
			errs[i] = fmt.Errorf("%s: %w", m.From, err)
			continue
		}
		done = append(done, m)
		a.printer.Printf("Moved %s -> %s\n", m.From, m.To)
	}
	if err := batchResult("move", len(moves), errs, paths); err != nil {
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(done)
	}
	return nil
}

// planTaggedMoves maps every object of the selection to its destination
// below dest: its base name, or with keepTree its path relative to the scope
// it was found in (a file scope keeps only its name). Objects already at
// their destination are left out.
func (a *App) planTaggedMoves(ctx context.Context, c *client.Client, scopes []string, dest string, f api.TagFilter, keepTree bool) ([]taggedMove, error) {
	if len(scopes) == 0 {
		scopes = []string{""}
	}
	var moves []taggedMove
	seen := map[string]bool{}
	for _, raw := range scopes {
		objs, err := a.selectTagged(ctx, c, []string{raw}, f)
		if err != nil {
			return nil, err
		}
		dir := strings.TrimLeft(raw, "/")
		if dir != "" && !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		for _, o := range objs {
			if seen[o.ID] {
				continue
			}
			seen[o.ID] = true
			rel := api.BaseName(o.Path)
			if keepTree && o.Path+"/" != dir {
				rel = strings.TrimPrefix(o.Path, dir)
			}
			to, err := remotePath(dest + rel)
			if err != nil {
				return nil, err
			}
			if to != o.Path {
				moves = append(moves, taggedMove{From: o.Path, To: to})
			}
		}
	}
	if len(seen) == 0 {
		return nil, errNothingTagged(f)
	}
	return moves, nil
}

// checkTaggedMoves reports every conflict among the planned moves at once:
// two objects with one destination, a destination that is another object
// being moved, a directory, or below a file, and (without force) an
// existing destination.
func checkTaggedMoves(ctx context.Context, c *client.Client, moves []taggedMove, force bool) error {
	sources := make(map[string]bool, len(moves))
	for _, m := range moves {
		sources[m.From] = true
	}
	var problems []string
	claimed := map[string]string{}
	isFile := map[string]bool{}
	fileAt := func(p string) (bool, error) {
		if v, ok := isFile[p]; ok {
			return v, nil
		}
		_, err := c.Stat(ctx, p)
		switch {
		case err == nil:
			isFile[p] = true
		case api.IsCode(err, api.CodeNotFound):
			isFile[p] = false
		default:
			return false, fmt.Errorf("check %s: %w", p, err)
		}
		return isFile[p], nil
	}
	for _, m := range moves {
		if other, ok := claimed[m.To]; ok {
			problems = append(problems, fmt.Sprintf("%s and %s would both move to %s", other, m.From, m.To))
			continue
		}
		claimed[m.To] = m.From
		if sources[m.To] {
			problems = append(problems, fmt.Sprintf("%s -> %s: the destination is also being moved", m.From, m.To))
			continue
		}
		exists, err := fileAt(m.To)
		if err != nil {
			return err
		}
		if exists && !force {
			problems = append(problems, fmt.Sprintf("%s -> %s: the destination exists", m.From, m.To))
		}
		for _, anc := range api.Ancestors(m.To) {
			file, err := fileAt(anc)
			if err != nil {
				return err
			}
			if file {
				problems = append(problems, fmt.Sprintf("%s -> %s: %s is a file", m.From, m.To, anc))
			}
		}
		l, err := c.List(ctx, client.ListOptions{Prefix: m.To + "/", Recursive: true, Limit: 1})
		if err != nil {
			return fmt.Errorf("check %s: %w", m.To, err)
		}
		if len(l.Objects) > 0 {
			problems = append(problems, fmt.Sprintf("%s -> %s: the destination is a directory", m.From, m.To))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	hint := ""
	if !force {
		hint = "use --force to overwrite existing destinations (they go to the trash)"
	}
	return &cliError{code: exitConflict, slug: string(api.CodeConflict),
		msg:  fmt.Sprintf("nothing moved: %d conflict(s):\n  %s", len(problems), strings.Join(problems, "\n  ")),
		hint: hint}
}

func (a *App) rmCommand() *cobra.Command {
	var recursive, purge, yes, broken, dryRun bool
	var byTag tagFlags
	cmd := &cobra.Command{
		Use:   "rm PATH...",
		Short: "Delete files (to the trash)",
		Long: "Move files to the trash, from where \"vela trash restore\" brings them back until they\n" +
			"are purged automatically. Directories (ending with /) need -r. --purge deletes for good\n" +
			"(admin keys only). -r and --purge ask for confirmation; -y skips it, and is required\n" +
			"when stdin is not a terminal. Removing a symlink never touches the file it points at;\n" +
			"removing a file breaks the symlinks pointing at it. --broken removes the broken\n" +
			"symlinks in the given directories (with -r, also below them).\n\n" +
			"--by-tag and --by-tags delete every object with these tags below the given paths\n" +
			"(everything by default). They always show the objects and ask unless -y is given;\n" +
			"--dry-run only lists them.",
		Example: "  vela rm notes/old.md\n  vela rm -r drafts/\n  vela rm --purge -y secret.pdf\n  vela rm --broken -r sorted/\n" +
			"  vela rm --by-tag draft --dry-run",
		Args: byTag.args(minArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if byTag.used() {
				if broken {
					return usagef("--broken cannot be combined with --by-tag")
				}
				return a.rmTagged(cmd, args, &byTag, purge, confirmFlags{yes: yes, dryRun: dryRun})
			}
			if dryRun {
				return usagef("--dry-run needs --by-tag or --by-tags")
			}
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
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --by-tag, only show what would be deleted")
	byTag.register(cmd)
	return cmd
}

// rmTagged deletes the objects of a tag selection after a confirmation.
func (a *App) rmTagged(cmd *cobra.Command, scopes []string, byTag *tagFlags, purge bool, conf confirmFlags) error {
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
	q := fmt.Sprintf("Move %d object(s) to the trash?", len(objs))
	if purge {
		q = fmt.Sprintf("Permanently delete %d object(s)?", len(objs))
	}
	if ok, err := a.confirmPlan(paths, objs, q, conf.yes, conf.dryRun); err != nil || !ok {
		return err
	}
	errs := make([]error, len(objs))
	results := []*api.DeleteResponse{}
	affected := 0
	for i, p := range paths {
		res, err := c.Delete(ctx, p, client.DeleteOptions{Purge: purge})
		if err != nil {
			errs[i] = fmt.Errorf("%s: %w", p, err)
			continue
		}
		results = append(results, res)
		affected += res.SymlinksAffected
		if purge {
			a.printer.Printf("Deleted %s permanently\n", p)
		} else {
			a.printer.Printf("Moved %s to the trash\n", p)
		}
	}
	a.warnSymlinksAffected(affected, purge)
	if err := batchResult("delete", len(paths), errs, paths); err != nil {
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(results)
	}
	if !purge {
		a.printer.Infof("Restore with: vela trash restore --by-tag ...")
	}
	return nil
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
		recursive                                             bool
		byTag                                                 tagFlags
	)
	cmd := &cobra.Command{
		Use:   "meta PATH... [flags]",
		Short: "Show or change metadata",
		Long: "Without flags, show the metadata of a file. With flags, change it. The change is applied\n" +
			"only if nobody modified the file in between (optimistic locking on the revision).\n\n" +
			"With -r, change files and everything below directories (they may be mixed, as with vis).\n" +
			"Tags are added and removed per object; --title and --content-type are refused, because one\n" +
			"value is almost never right for many files. Objects that already match are left alone.\n\n" +
			"--by-tag and --by-tags change every object with these tags below the given paths\n" +
			"(everything by default), with the same rules as -r.",
		Example: "  vela meta report.pdf\n  vela meta report.pdf --title 'Q1 report' --tag q1 --untag draft\n" +
			"  vela meta report.pdf --expires 7d\n  vela meta report.pdf --expires none --disposition attachment\n" +
			"  vela meta -r photos/2026/ --tag trip --untag inbox\n  vela meta --by-tag inbox --tag sorted --untag inbox",
		Args:              byTag.args(minArgs(1)),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			changed := cmd.Flags().Changed
			tagged := byTag.used()
			many := recursive || tagged
			if !many && len(args) != 1 {
				return usagef("meta takes one path (use -r for several)")
			}
			if many {
				for _, name := range []string{"title", "content-type"} {
					if changed(name) {
						return usagef("--%s cannot be used with -r or --by-tag", name)
					}
				}
			}
			// Validate every flag before any request, so that a typo never
			// leaves a batch half applied.
			var base api.ObjectPatch
			if changed("title") {
				if err := api.ValidateTitle(title); err != nil {
					return usageError(err)
				}
				base.Title = api.Some(title)
			}
			if changed("description") {
				if err := api.ValidateDescription(description); err != nil {
					return usageError(err)
				}
				base.Description = api.Some(description)
			}
			if changed("content-type") {
				base.ContentType = api.Some(contentType)
			}
			if changed("disposition") {
				d, err := api.ParseDisposition(disposition)
				if err != nil {
					return usageError(err)
				}
				base.Disposition = api.Some(d)
			}
			if changed("expires") {
				t, err := api.ParseExpiry(expires, a.Now())
				if err != nil {
					return usageError(err)
				}
				if t == nil {
					base.ExpiresAt = api.Null[time.Time]()
				} else {
					base.ExpiresAt = api.Some(*t)
				}
			}
			add, err := api.NormalizeTags(tags)
			if err != nil {
				return usageError(err)
			}
			remove, err := api.NormalizeTags(untags)
			if err != nil {
				return usageError(err)
			}
			editTags := len(tags) > 0 || len(untags) > 0
			if many && base.IsEmpty() && !editTags {
				return usagef("meta -r and --by-tag need at least one change (use ls -l -r or stat --by-tag to show metadata)")
			}
			patchFor := func(cur api.Object) api.ObjectPatch {
				patch := base
				if editTags {
					patch.Tags = api.Some(applyTags(cur.Tags, add, remove))
				}
				return patch
			}

			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if many {
				targets, err := a.batchTargets(ctx, c, args, true, &byTag)
				if err != nil {
					return err
				}
				updated, n, err := applyPatches(ctx, c, "metadata change", targets, func(o api.Object) api.ObjectPatch {
					return dropUnchanged(patchFor(o), o)
				})
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.printer.JSONValue(updated)
				}
				a.printer.Printf("%d object(s) checked (%d changed)\n", len(targets), n)
				return nil
			}

			path, err := remotePath(args[0])
			if err != nil {
				return err
			}
			cur, err := c.Stat(ctx, path)
			if err != nil {
				return a.notFoundHint(ctx, c, err, path)
			}
			patch := patchFor(*cur)
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
	f.BoolVarP(&recursive, "recursive", "r", false, "apply to several paths and everything below directories")
	byTag.register(cmd)
	return cmd
}

// applyTags adds and then removes tags, so that --untag wins over --tag.
func applyTags(cur, add, remove []string) []string {
	next := slices.Clone(cur)
	for _, t := range add {
		if !slices.Contains(next, t) {
			next = append(next, t)
		}
	}
	return slices.DeleteFunc(next, func(t string) bool { return slices.Contains(remove, t) })
}

// dropUnchanged clears the fields of patch that o already has, so that a
// batch does not bump revisions of objects it would not change.
func dropUnchanged(patch api.ObjectPatch, o api.Object) api.ObjectPatch {
	if patch.Description.HasValue() && patch.Description.Value == o.Description {
		patch.Description = api.Optional[string]{}
	}
	if patch.Disposition.HasValue() && patch.Disposition.Value == o.Disposition {
		patch.Disposition = api.Optional[api.Disposition]{}
	}
	if patch.Tags.HasValue() && slices.Equal(patch.Tags.Value, o.Tags) {
		patch.Tags = api.Optional[[]string]{}
	}
	switch {
	case patch.ExpiresAt.Null && o.ExpiresAt == nil,
		patch.ExpiresAt.HasValue() && o.ExpiresAt != nil && patch.ExpiresAt.Value.Equal(*o.ExpiresAt):
		patch.ExpiresAt = api.Optional[time.Time]{}
	}
	return patch
}

func (a *App) visCommand() *cobra.Command {
	var recursive bool
	var byTag tagFlags
	cmd := &cobra.Command{
		Use:   "vis PATH... public|unlisted|private",
		Short: "Change visibility",
		Long: "Change the visibility of files, or with -r of everything below directories (files and\n" +
			"directories may be mixed). A path that matches nothing is an error. The change takes effect\n" +
			"immediately. Unlisted is not access control: anyone with the URL can read it.\n\n" +
			"--by-tag and --by-tags change every object with these tags below the given paths\n" +
			"(everything by default).",
		Example: "  vela vis report.pdf private\n  vela vis -r docs/math/ public\n  vela vis --by-tag handout public",
		Args: func(cmd *cobra.Command, args []string) error {
			// A tag selection replaces the paths, leaving only the visibility.
			if byTag.used() {
				return minArgs(1)(cmd, args)
			}
			return minArgs(2)(cmd, args)
		},
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
			targets, err := a.batchTargets(ctx, c, args[:len(args)-1], recursive, &byTag)
			if err != nil {
				return err
			}
			return a.applyVisibility(ctx, c, targets, vis)
		},
		ValidArgsFunction: a.completeRemote(),
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "apply to everything below directories")
	byTag.register(cmd)
	return cmd
}

// batchTargets resolves every argument of a batch command before anything
// changes, so that a typo cannot leave the command half done or report a
// hollow success. With a tag selection, the arguments are its scopes.
func (a *App) batchTargets(ctx context.Context, c *client.Client, args []string, recursive bool, byTag *tagFlags) ([]api.Object, error) {
	if byTag.used() {
		f, err := byTag.filter()
		if err != nil {
			return nil, err
		}
		objs, err := a.selectTagged(ctx, c, args, f)
		if err != nil {
			return nil, err
		}
		if len(objs) == 0 {
			return nil, errNothingTagged(f)
		}
		return objs, nil
	}
	var targets []api.Object
	for _, raw := range args {
		objs, err := a.resolveTargets(ctx, c, raw, recursive)
		if err != nil {
			return nil, err
		}
		targets = append(targets, objs...)
	}
	return uniqueObjects(targets), nil
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
