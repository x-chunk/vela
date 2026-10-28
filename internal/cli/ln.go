package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
)

// lnFlags are the options of vela ln. Metadata flags mirror put, except that
// an absent visibility flag means "copy it from the source", not the
// profile's default visibility.
type lnFlags struct {
	recursive, force, dryRun  bool
	copyMeta, noCopyMeta      bool
	public, unlisted, private bool
	title, description        string
	tags                      []string
	disposition, expires      string
	symbolic                  bool
	keepTree                  bool
	skip, skipExplicitly      bool
}

func (a *App) lnCommand() *cobra.Command {
	var f lnFlags
	var byTag tagFlags
	cmd := &cobra.Command{
		Use:   "ln SOURCE... DEST",
		Short: "Create symlinks to files",
		Long: "Create symlinks: entries that serve the content of another file. A symlink refers to\n" +
			"its file by ID, not by path, so renaming or moving the file keeps it working and\n" +
			"overwriting the file changes what it serves. Deleting the file breaks it (vela ls -l\n" +
			"shows it as broken); restoring the file from the trash repairs it.\n\n" +
			"A symlink has its own path, visibility, title, tags and expiry. They are copied from\n" +
			"SOURCE once, when the symlink is created (--no-copy-meta skips that); later changes\n" +
			"to either side do not propagate. Anonymous visitors get the stricter of the two\n" +
			"visibilities, so a public symlink never publishes a private file. A SOURCE that is\n" +
			"itself a symlink is followed: the new symlink points at the same file.\n\n" +
			"With several SOURCEs, DEST must be a directory (ending with /). -r links every file\n" +
			"under a directory at once; files added to it later are not linked. -f replaces an\n" +
			"existing DEST: a symlink is retargeted in place (its URL and signed links follow the\n" +
			"new file), a file goes to the trash.\n\n" +
			"With --by-tag or --by-tags, \"ln [SCOPE...] DEST/\" links every object with these tags\n" +
			"below SCOPE (everything by default) into the directory DEST: each under its own name,\n" +
			"or with --save-structure under its path relative to SCOPE. Every conflict (two objects\n" +
			"with one destination, an existing destination without -f) is reported before the\n" +
			"first symlink is created, and nothing is linked. --skip links the rest and silently\n" +
			"leaves the conflicting objects out; --skip-explicitly also reports each one.",
		Example: "  vela ln originals/a.jpg sorted/travel/a.jpg\n" +
			"  vela ln originals/a.jpg originals/b.jpg sorted/travel/\n" +
			"  vela ln -r originals/2026/ sorted/2026/\n" +
			"  vela ln -f originals/new.pdf latest.pdf\n" +
			"  vela ln --by-tag travel originals/ sorted/travel/ --skip-explicitly",
		Args: byTag.args(minArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := f.request(cmd, a)
			if err != nil {
				return err
			}
			if byTag.used() {
				if f.recursive {
					return usagef("-r cannot be combined with --by-tag or --by-tags")
				}
				if len(args) == 0 {
					return usagef("%s --by-tag needs a destination directory", cmd.CommandPath())
				}
				return a.lnTagged(cmd, f, req, args[:len(args)-1], args[len(args)-1], &byTag)
			}
			if f.keepTree || f.skip || f.skipExplicitly {
				return usagef("--save-structure, --skip and --skip-explicitly need --by-tag or --by-tags")
			}
			sources, dest := args[:len(args)-1], strings.TrimLeft(args[len(args)-1], "/")
			if f.recursive {
				if len(sources) != 1 {
					return usagef("-r takes exactly one SOURCE directory")
				}
				return a.lnTree(cmd, f, req, sources[0], dest)
			}
			if len(sources) > 1 && dest != "" && !strings.HasSuffix(dest, "/") {
				return usagef("with several sources, DEST must be a directory ending with /")
			}
			pairs := make([][2]string, len(sources))
			for i, raw := range sources {
				src, isDir, err := api.CleanPath(raw)
				if err != nil {
					return usageError(err)
				}
				if isDir {
					return usagef("%s is a directory (use -r)", src)
				}
				to := dest
				if to == "" || strings.HasSuffix(to, "/") {
					to += api.BaseName(src)
				}
				if to, err = remotePath(to); err != nil {
					return err
				}
				pairs[i] = [2]string{src, to}
			}
			return a.lnFiles(cmd, f, req, pairs)
		},
		ValidArgsFunction: a.completeRemote(),
	}
	fl := cmd.Flags()
	fl.BoolVarP(&f.recursive, "recursive", "r", false, "link every file under a directory")
	fl.BoolVarP(&f.force, "force", "f", false, "replace existing destinations (retarget symlinks, trash files)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "show what would be linked without changing anything")
	fl.BoolVar(&f.copyMeta, "copy-meta", false, "copy the source's metadata again when retargeting with -f")
	fl.BoolVar(&f.noCopyMeta, "no-copy-meta", false, "do not copy the source's metadata to new symlinks")
	cmd.MarkFlagsMutuallyExclusive("copy-meta", "no-copy-meta")
	fl.BoolVar(&f.public, "public", false, "listed in the index and served by URL")
	fl.BoolVar(&f.unlisted, "unlisted", false, "served by URL but not listed (not access control!)")
	fl.BoolVar(&f.private, "private", false, "only via API key or signed link (vela share)")
	cmd.MarkFlagsMutuallyExclusive("public", "unlisted", "private")
	fl.StringVar(&f.title, "title", "", "title shown in the index")
	fl.StringVar(&f.description, "description", "", "description shown in the index")
	fl.StringArrayVar(&f.tags, "tag", nil, "tag (repeatable; replaces the copied tags)")
	fl.StringVar(&f.disposition, "disposition", "", "inline or attachment (download)")
	fl.StringVar(&f.expires, "expires", "", "delete the symlink automatically after a duration (7d) or at an RFC 3339 time")
	// Muscle memory from ln -s: accepted and ignored, every Vela link is symbolic.
	fl.BoolVarP(&f.symbolic, "symbolic", "s", false, "accepted for compatibility with ln -s; has no effect")
	_ = fl.MarkHidden("symbolic")
	fl.BoolVar(&f.keepTree, "save-structure", false, "with --by-tag, keep paths relative to SCOPE instead of linking flat")
	fl.BoolVar(&f.skip, "skip", false, "with --by-tag, leave conflicting objects out silently and link the rest")
	fl.BoolVar(&f.skipExplicitly, "skip-explicitly", false, "with --by-tag, leave conflicting objects out, report each, and link the rest")
	cmd.MarkFlagsMutuallyExclusive("skip", "skip-explicitly")
	byTag.register(cmd)
	return cmd
}

// request builds the metadata part of a symlink request from the flags.
func (f *lnFlags) request(cmd *cobra.Command, a *App) (api.CreateSymlinkRequest, error) {
	req := api.CreateSymlinkRequest{Overwrite: f.force}
	switch {
	case f.copyMeta:
		req.CopyMeta = ptrTo(true)
	case f.noCopyMeta:
		req.CopyMeta = ptrTo(false)
	}
	switch {
	case f.public:
		req.Visibility = ptrTo(api.Public)
	case f.unlisted:
		req.Visibility = ptrTo(api.Unlisted)
	case f.private:
		req.Visibility = ptrTo(api.Private)
	}
	changed := cmd.Flags().Changed
	if changed("title") {
		if err := api.ValidateTitle(f.title); err != nil {
			return req, usageError(err)
		}
		req.Title = &f.title
	}
	if changed("description") {
		if err := api.ValidateDescription(f.description); err != nil {
			return req, usageError(err)
		}
		req.Description = &f.description
	}
	if len(f.tags) > 0 {
		tags, err := api.NormalizeTags(f.tags)
		if err != nil {
			return req, usageError(err)
		}
		req.Tags = &tags
	}
	if f.disposition != "" {
		d, err := api.ParseDisposition(f.disposition)
		if err != nil {
			return req, usageError(err)
		}
		req.Disposition = &d
	}
	if f.expires != "" {
		t, err := api.ParseExpiry(f.expires, a.Now())
		if err != nil {
			return req, usageError(err)
		}
		if t == nil {
			return req, usagef("--expires none makes no sense for a new symlink; omit the flag")
		}
		req.ExpiresAt = t
	}
	return req, nil
}

func ptrTo[T any](v T) *T { return &v }

func (a *App) lnFiles(cmd *cobra.Command, f lnFlags, req api.CreateSymlinkRequest, pairs [][2]string) error {
	c, _, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	errs := make([]error, len(pairs))
	paths := make([]string, len(pairs))
	var results []*api.Object
	for i, pair := range pairs {
		paths[i] = pair[0]
		if f.dryRun {
			if _, err := c.Stat(ctx, pair[0]); err != nil {
				errs[i] = fmt.Errorf("%s: %w", pair[0], a.symlinkError(ctx, c, err, pair[0]))
				continue
			}
			a.printer.Printf("would link %s -> %s\n", pair[1], pair[0])
			continue
		}
		r := req
		r.Source, r.Path = pair[0], pair[1]
		o, _, err := c.CreateSymlink(ctx, r)
		if err != nil {
			errs[i] = fmt.Errorf("%s: %w", pair[0], a.symlinkError(ctx, c, err, pair[0]))
			continue
		}
		results = append(results, o)
		a.printSymlink(o)
	}
	if err := batchResult("link", len(pairs), errs, paths); err != nil {
		return err
	}
	switch {
	case !a.jsonOut || f.dryRun:
		return nil
	case len(results) == 1:
		return a.printer.JSONValue(results[0])
	default:
		return a.printer.JSONValue(results)
	}
}

// lnTagged links the objects of a tag selection into the directory dest. It
// plans every symlink and checks every destination before creating the
// first one: a conflict refuses the whole plan unless --skip or
// --skip-explicitly drops the conflicting entries.
func (a *App) lnTagged(cmd *cobra.Command, f lnFlags, req api.CreateSymlinkRequest, scopes []string, dest string, byTag *tagFlags) error {
	filter, err := byTag.filter()
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
	plan, err := a.planTaggedMoves(ctx, c, scopes, dest, filter, f.keepTree)
	if err == nil {
		var conflicts []planConflict
		if conflicts, err = taggedConflicts(ctx, c, plan, f.force, "link", "linked"); err == nil {
			plan, err = a.resolveLinkConflicts(f, plan, conflicts)
		}
	}
	cancel()
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		a.printer.Infof("Nothing to link.")
		if a.jsonOut && !f.dryRun {
			return a.printer.JSONValue([]api.Object{})
		}
		return nil
	}
	pairs := make([][2]string, len(plan))
	for i, m := range plan {
		pairs[i] = [2]string{m.From, m.To}
	}
	return a.lnFiles(cmd, f, req, pairs)
}

// resolveLinkConflicts refuses a plan with conflicts, or with --skip and
// --skip-explicitly returns it without the conflicting entries.
func (a *App) resolveLinkConflicts(f lnFlags, plan []taggedMove, conflicts []planConflict) ([]taggedMove, error) {
	if len(conflicts) == 0 {
		return plan, nil
	}
	if !f.skip && !f.skipExplicitly {
		hint := "use --skip or --skip-explicitly to link the rest"
		if !f.force {
			hint += ", -f to replace existing destinations"
		}
		return nil, conflictError("nothing linked", conflicts, hint)
	}
	drop := make(map[int]bool, len(conflicts))
	for _, p := range conflicts {
		drop[p.index] = true
		if f.skipExplicitly {
			a.printer.Warnf("skipped %s", p.msg)
		}
	}
	kept := plan[:0:0]
	for i, m := range plan {
		if !drop[i] {
			kept = append(kept, m)
		}
	}
	return kept, nil
}

func (a *App) lnTree(cmd *cobra.Command, f lnFlags, req api.CreateSymlinkRequest, rawSource, rawDest string) error {
	if !strings.HasSuffix(rawSource, "/") {
		rawSource += "/"
	}
	if rawDest != "" && !strings.HasSuffix(rawDest, "/") {
		rawDest += "/"
	}
	source, err := remotePrefix(rawSource)
	if err != nil {
		return err
	}
	dest, err := remotePrefix(rawDest)
	if err != nil {
		return err
	}
	c, _, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	if f.dryRun {
		l, err := c.ListAll(ctx, client.ListOptions{Prefix: source, Recursive: true})
		if err != nil {
			return err
		}
		for _, o := range l.Objects {
			if o.Kind == api.KindSymlink && (o.Target == nil || o.Target.State != api.TargetOK) {
				a.printer.Printf("would skip %s (broken symlink)\n", o.Path)
				continue
			}
			a.printer.Printf("would link %s -> %s\n", dest+strings.TrimPrefix(o.Path, source), o.Path)
		}
		return nil
	}
	req.Source, req.Path = source, dest
	res, err := c.CreateSymlinkTree(ctx, req)
	if err != nil {
		err = hintFor(err, api.CodeAlreadyExists, "use -f to replace them (symlinks are retargeted, files go to the trash)")
		return a.symlinkError(ctx, c, err, source)
	}
	if a.jsonOut {
		return a.printer.JSONValue(res)
	}
	a.printer.Printf("Linked %s -> %s: %d created, %d replaced\n", dest, source, res.Created, res.Replaced)
	for _, p := range res.Skipped {
		a.printer.Warnf("skipped broken symlink %s", p)
	}
	a.printer.Infof("Files added to %s later are not linked automatically.", source)
	return nil
}

// printSymlink reports a created or retargeted symlink and where it is
// reachable.
func (a *App) printSymlink(o *api.Object) {
	target := "?"
	if o.Target != nil && o.Target.Path != nil {
		target = *o.Target.Path
	}
	a.printer.Printf("%s -> %s\n", o.Path, target)
	if o.URL != nil {
		a.printer.Infof("%s", *o.URL)
	} else {
		a.printer.Infof("private; create a temporary link with: vela share %s", o.Path)
	}
	if o.EffectiveVisibility != o.Visibility && o.Target != nil && o.Target.Visibility != nil {
		a.printer.Warnf("%s is %s, but its effective visibility is %s (the target is %s)",
			o.Path, o.Visibility, o.EffectiveVisibility, *o.Target.Visibility)
	}
}

// symlinkError explains failures that are specific to symlinks: a server too
// old to know them answers 404 for the whole endpoint.
func (a *App) symlinkError(ctx context.Context, c *client.Client, err error, source string) error {
	if !api.IsCode(err, api.CodeNotFound) {
		if api.IsCode(err, api.CodeInvalidRequest) && strings.Contains(err.Error(), "is a directory") {
			return withHint(err, "use -r to link a directory")
		}
		return err
	}
	if info, ierr := c.Info(ctx); ierr == nil && !info.Features.Symlinks {
		return &cliError{code: exitError, slug: "unsupported", msg: "the server does not support symlinks",
			hint: "upgrade velad to a version with symlink support"}
	}
	return a.notFoundHint(ctx, c, err, source)
}
