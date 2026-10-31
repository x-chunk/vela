package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
)

// lnFlags are the options of vela ln. Metadata flags mirror put, except that
// an absent visibility flag means "copy it from the source", not the
// profile's default visibility.
type lnFlags struct {
	recursive, force, dryRun bool
	copyMeta, noCopyMeta     bool
	// copyMetaRaw is the value of --copy-meta: "all" or a field list.
	copyMetaRaw               string
	metaFields                []metaField
	public, unlisted, private bool
	title, description        string
	tags                      []string
	disposition, expires      string
	symbolic                  bool
	keepTree                  bool
	skip, skipExplicitly      bool
	skipSymlinks              bool
	yes                       bool
	sync, prune, strict       bool
	pruneTrashed              bool
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
			"leaves the conflicting objects out; --skip-explicitly also reports each one.\n" +
			"--skip-symlinks leaves symlinks out of the selection: new symlinks copy their file's\n" +
			"tags, so a repeated run would otherwise select them too. The symlinks are shown and\n" +
			"need a confirmation unless -y is given.\n\n" +
			"--sync makes a repeated run link only what is missing: symlinks already in DEST that\n" +
			"point at a selected file count as done, wherever they are, and symlinks are never\n" +
			"selected (--skip-symlinks is implied). --prune also moves to the trash the symlinks in\n" +
			"DEST whose file lies in SCOPE but is no longer selected; with --prune-trashed also\n" +
			"those whose file is in the trash (restoring the file would have repaired them, but\n" +
			"a new symlink gets a new URL and loses the old one's signed links).\n" +
			"Symlinks to files outside SCOPE are kept, unless the file still has the tags: then it\n" +
			"was most likely moved out of SCOPE, and its symlink is pruned too. Files are never\n" +
			"touched. A new symlink may take the place of a pruned one, with a warning; --strict\n" +
			"refuses that as a conflict instead. --sync --copy-meta copies the metadata of each\n" +
			"selected file again to its symlinks in DEST whose title, description, tags,\n" +
			"disposition or visibility differ, in place: their URLs and signed links keep working,\n" +
			"and changes made to the symlinks themselves are overwritten.",
		Example: "  vela ln originals/a.jpg sorted/travel/a.jpg\n" +
			"  vela ln originals/a.jpg originals/b.jpg sorted/travel/\n" +
			"  vela ln -r originals/2026/ sorted/2026/\n" +
			"  vela ln -f originals/new.pdf latest.pdf\n" +
			"  vela ln --by-tag travel originals/ sorted/travel/ --skip-explicitly\n" +
			"  vela ln --by-tag travel originals/ sorted/travel/ --save-structure --sync -y",
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
				if f.prune && !f.sync {
					return usagef("--prune needs --sync")
				}
				if (f.strict || f.pruneTrashed) && !f.prune {
					return usagef("--strict and --prune-trashed need --prune")
				}
				return a.lnTagged(cmd, f, req, args[:len(args)-1], args[len(args)-1], &byTag)
			}
			if err := f.tagOnly(); err != nil {
				return err
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
	fl.StringVar(&f.copyMetaRaw, "copy-meta", "", "copy the source's metadata again, when retargeting with -f or to every symlink with --sync\n"+
		"(all, or with --sync a list of title, description, tags, disposition, visibility)")
	fl.Lookup("copy-meta").NoOptDefVal = "all"
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
	fl.BoolVarP(&f.yes, "yes", "y", false, "with --by-tag, link without asking")
	fl.BoolVar(&f.sync, "sync", false, "with --by-tag, link only files that have no symlink in DEST yet")
	fl.BoolVar(&f.prune, "prune", false, "with --sync, trash symlinks in DEST to files in SCOPE that are no longer selected")
	fl.BoolVar(&f.pruneTrashed, "prune-trashed", false, "with --prune, also trash symlinks to files in the trash")
	fl.BoolVar(&f.strict, "strict", false, "with --prune, treat the place of a pruned symlink as taken")
	fl.BoolVar(&f.skipSymlinks, "skip-symlinks", false, "with --by-tag, leave symlinks out of the selection")
	byTag.register(cmd)
	return cmd
}

// tagOnly rejects flags that only apply to a tag selection, naming the
// ones that were given rather than every flag of the group.
func (f *lnFlags) tagOnly() error {
	var used []string
	for _, fl := range []struct {
		name string
		set  bool
	}{
		{"--save-structure", f.keepTree},
		{"--skip", f.skip},
		{"--skip-explicitly", f.skipExplicitly},
		{"--skip-symlinks", f.skipSymlinks},
		{"--sync", f.sync},
		{"--prune", f.prune},
		{"--prune-trashed", f.pruneTrashed},
		{"--strict", f.strict},
		{"-y", f.yes},
	} {
		if fl.set {
			used = append(used, fl.name)
		}
	}
	switch len(used) {
	case 0:
		return nil
	case 1:
		return usagef("%s needs --by-tag or --by-tags", used[0])
	default:
		return usagef("%s need --by-tag or --by-tags", strings.Join(used, ", "))
	}
}

// request builds the metadata part of a symlink request from the flags.
func (f *lnFlags) request(cmd *cobra.Command, a *App) (api.CreateSymlinkRequest, error) {
	req := api.CreateSymlinkRequest{Overwrite: f.force}
	if cmd.Flags().Changed("copy-meta") {
		fields, err := parseMetaFields(f.copyMetaRaw)
		if err != nil {
			return req, err
		}
		if len(fields) < len(allMetaFields) && !f.sync {
			return req, usagef("--copy-meta with a field list needs --by-tag and --sync")
		}
		f.copyMeta, f.metaFields = true, fields
	}
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
	var keep func(api.Object) bool
	if f.skipSymlinks || f.sync {
		keep = func(o api.Object) bool { return o.Kind != api.KindSymlink }
	}
	plan, err := a.planTaggedMoves(ctx, c, scopes, dest, filter, f.keepTree, keep)
	upToDate := 0
	var prune []linkPrune
	var updates []linkUpdate
	if err == nil && f.sync {
		var l *api.Listing
		if l, err = c.ListAll(ctx, client.ListOptions{Prefix: dest, Recursive: true, Limit: 1000}); err != nil {
			err = fmt.Errorf("list %s: %w", dest, err)
		} else {
			if f.copyMeta {
				updates = metaUpdates(l.Objects, plan, f.metaFields)
			}
			if f.prune {
				prune = pruneLinks(l.Objects, plan, scopes, f.pruneTrashed)
				var moved []linkPrune
				if moved, err = movedOutLinks(ctx, c, l.Objects, scopes, filter); err == nil {
					prune = append(prune, moved...)
				}
			}
			plan, upToDate = a.syncLinkPlan(l.Objects, plan)
		}
	}
	if err == nil {
		var conflicts []planConflict
		if conflicts, err = taggedConflicts(ctx, c, plan, f.force, "link", "linked"); err == nil {
			if !f.strict {
				conflicts = a.freedByPrune(plan, conflicts, prune)
			}
			plan, err = a.resolveLinkConflicts(f, plan, conflicts)
		}
	}
	cancel()
	if err != nil {
		return err
	}
	if f.sync && upToDate > 0 {
		a.printer.Infof("%d symlink(s) up to date.", upToDate)
	}
	if f.prune || f.sync && f.copyMeta {
		return a.lnSyncApply(cmd, f, req, plan, prune, updates)
	}
	if len(plan) == 0 {
		a.printer.Infof("Nothing to link.")
		if a.jsonOut && !f.dryRun {
			return a.printer.JSONValue([]api.Object{})
		}
		return nil
	}
	pairs := make([][2]string, len(plan))
	lines := make([]string, len(plan))
	for i, m := range plan {
		pairs[i] = [2]string{m.From, m.To}
		lines[i] = m.To + " -> " + m.From
	}
	// A tag can reach files the user did not think of, and with -f an
	// existing destination file goes to the trash, so the plan is confirmed
	// as in mv --by-tag.
	if ok, err := a.confirmPlan(lines, plan, fmt.Sprintf("Create %d symlink(s)?", len(plan)), f.yes, f.dryRun); err != nil || !ok {
		return err
	}
	return a.lnFiles(cmd, f, req, pairs)
}

// syncLinkPlan drops the planned symlinks whose file already has a symlink
// in dest and returns how many were dropped. A symlink at another path
// than planned (the file was moved or renamed since) is kept and reported
// instead of getting a duplicate.
func (a *App) syncLinkPlan(existing []api.Object, plan []taggedMove) ([]taggedMove, int) {
	linked := map[string]string{}
	for _, o := range existing {
		if o.Kind != api.KindSymlink || o.Target == nil {
			continue
		}
		// Prefer the planned path when one file has several symlinks.
		if _, ok := linked[o.Target.ID]; !ok {
			linked[o.Target.ID] = o.Path
		}
	}
	planned := make(map[string]string, len(plan))
	for _, m := range plan {
		planned[m.id] = m.To
	}
	for _, o := range existing {
		if o.Kind == api.KindSymlink && o.Target != nil && planned[o.Target.ID] == o.Path {
			linked[o.Target.ID] = o.Path
		}
	}
	kept := plan[:0:0]
	upToDate := 0
	for _, m := range plan {
		at, ok := linked[m.id]
		switch {
		case !ok:
			kept = append(kept, m)
		case at == m.To:
			upToDate++
		default:
			upToDate++
			a.printer.Warnf("%s is already linked at %s, not at %s", m.From, at, m.To)
		}
	}
	return kept, upToDate
}

// linkPrune is a symlink that ln --sync --prune moves to the trash.
type linkPrune struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// pruneLinks returns the symlinks among existing (the contents of DEST)
// whose file lies in one of scopes but is not in plan: it lost its tags or,
// with trashed, is in the trash. Without trashed such a symlink is kept,
// because restoring the file repairs it with its URL and signed links. A
// gone target is left alone, because "gone" also means
// outside the key's prefix, where the symlink may well work for others;
// vela rm --broken removes such symlinks deliberately.
func pruneLinks(existing []api.Object, plan []taggedMove, scopes []string, trashed bool) []linkPrune {
	selected := make(map[string]bool, len(plan))
	for _, m := range plan {
		selected[m.id] = true
	}
	var out []linkPrune
	for _, o := range existing {
		t := o.Target
		if o.Kind != api.KindSymlink || t == nil || t.Path == nil || selected[t.ID] || !inScopes(*t.Path, scopes) {
			continue
		}
		switch t.State {
		case api.TargetOK:
			out = append(out, linkPrune{Path: o.Path, Reason: "no longer selected"})
		case api.TargetTrashed:
			if !trashed {
				continue
			}
			out = append(out, linkPrune{Path: o.Path, Reason: "the file is in the trash"})
		}
	}
	return out
}

// freedByPrune drops the conflicts of destinations that a pruned symlink
// occupies: the symlinks are trashed before any is created, so the place is
// free by then. Each such replacement is reported.
func (a *App) freedByPrune(plan []taggedMove, conflicts []planConflict, prune []linkPrune) []planConflict {
	pruned := make(map[string]bool, len(prune))
	for _, p := range prune {
		pruned[p.Path] = true
	}
	kept := conflicts[:0:0]
	for _, p := range conflicts {
		if m := plan[p.index]; p.exists && pruned[m.To] {
			a.printer.Warnf("%s takes the place of the pruned symlink %s", m.From, m.To)
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

// movedOutLinks returns the symlinks among existing whose file lies outside
// scopes but still matches f: the file was most likely selected once and
// moved out since, so the symlink is stale. A symlink to any other file
// outside scopes is someone's own and is kept.
func movedOutLinks(ctx context.Context, c *client.Client, existing []api.Object, scopes []string, f api.TagFilter) ([]linkPrune, error) {
	var out []linkPrune
	for _, o := range existing {
		t := o.Target
		if o.Kind != api.KindSymlink || t == nil || t.Path == nil || t.State != api.TargetOK || inScopes(*t.Path, scopes) {
			continue
		}
		target, err := c.Stat(ctx, *t.Path)
		switch {
		case api.IsCode(err, api.CodeNotFound):
			continue
		case err != nil:
			return nil, fmt.Errorf("check %s: %w", *t.Path, err)
		}
		if target.ID == t.ID && f.Match(target.Tags) {
			out = append(out, linkPrune{Path: o.Path, Reason: "the file moved out of scope"})
		}
	}
	return out, nil
}

// inScopes reports whether p is one of the file scopes or below one of the
// directory scopes; no scopes means everything.
func inScopes(p string, scopes []string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, raw := range scopes {
		s := strings.TrimLeft(raw, "/")
		if s == "" || s == p || strings.HasPrefix(p, strings.TrimSuffix(s, "/")+"/") {
			return true
		}
	}
	return false
}

// metaField is one piece of metadata that ln --sync --copy-meta compares
// and copies.
type metaField struct {
	name string
	same func(link, file *api.Object) bool
	set  func(r *api.CreateSymlinkRequest, file *api.Object)
}

// allMetaFields are the fields a new symlink copies from its file (D3).
var allMetaFields = []metaField{
	{"title", func(l, o *api.Object) bool { return l.Title == o.Title },
		func(r *api.CreateSymlinkRequest, o *api.Object) { r.Title = ptrTo(o.Title) }},
	{"description", func(l, o *api.Object) bool { return l.Description == o.Description },
		func(r *api.CreateSymlinkRequest, o *api.Object) { r.Description = ptrTo(o.Description) }},
	{"tags", func(l, o *api.Object) bool { return slices.Equal(l.Tags, o.Tags) },
		func(r *api.CreateSymlinkRequest, o *api.Object) { r.Tags = ptrTo(slices.Clone(o.Tags)) }},
	{"disposition", func(l, o *api.Object) bool { return l.Disposition == o.Disposition },
		func(r *api.CreateSymlinkRequest, o *api.Object) { r.Disposition = ptrTo(o.Disposition) }},
	{"visibility", func(l, o *api.Object) bool { return l.Visibility == o.Visibility },
		func(r *api.CreateSymlinkRequest, o *api.Object) { r.Visibility = ptrTo(o.Visibility) }},
}

// parseMetaFields parses the value of --copy-meta: "all" (or "true", for
// those who spell out the old boolean) or a comma-separated list of fields.
func parseMetaFields(raw string) ([]metaField, error) {
	switch strings.TrimSpace(raw) {
	case "all", "true":
		return allMetaFields, nil
	}
	var out []metaField
	for name := range strings.SplitSeq(raw, ",") {
		name = strings.TrimSpace(name)
		i := slices.IndexFunc(allMetaFields, func(m metaField) bool { return m.name == name })
		if i < 0 {
			return nil, usagef("--copy-meta: unknown field %q (use title, description, tags, disposition, visibility or all)", name)
		}
		if !slices.ContainsFunc(out, func(m metaField) bool { return m.name == name }) {
			out = append(out, allMetaFields[i])
		}
	}
	return out, nil
}

// linkUpdate is a symlink whose metadata ln --sync --copy-meta copies
// again from its file.
type linkUpdate struct {
	Path   string   `json:"path"`
	Source string   `json:"source"`
	Fields []string `json:"fields"`
	req    api.CreateSymlinkRequest
}

// metaUpdates returns the symlinks among existing that point at a file of
// plan and differ from it in any of fields. Each update retargets the
// symlink in place to the same file with those fields set explicitly, so
// the server keeps its ID and nothing else changes.
func metaUpdates(existing []api.Object, plan []taggedMove, fields []metaField) []linkUpdate {
	files := make(map[string]*api.Object, len(plan))
	for i := range plan {
		files[plan[i].id] = &plan[i].obj
	}
	var out []linkUpdate
	for i := range existing {
		l := &existing[i]
		if l.Kind != api.KindSymlink || l.Target == nil || l.Target.State != api.TargetOK {
			continue
		}
		file, ok := files[l.Target.ID]
		if !ok {
			continue
		}
		u := linkUpdate{Path: l.Path, Source: file.Path,
			req: api.CreateSymlinkRequest{Source: file.Path, Path: l.Path, Overwrite: true, CopyMeta: ptrTo(false)}}
		for _, fl := range fields {
			if !fl.same(l, file) {
				u.Fields = append(u.Fields, fl.name)
				fl.set(&u.req, file)
			}
		}
		if len(u.Fields) > 0 {
			out = append(out, u)
		}
	}
	return out
}

// lnSyncResult is the --json result of ln --sync with --prune or
// --copy-meta.
type lnSyncResult struct {
	Created []*api.Object `json:"created"`
	Updated []*api.Object `json:"updated"`
	Pruned  []string      `json:"pruned"`
}

// lnSyncPlan is the --dry-run --json plan of ln --sync with --prune or
// --copy-meta.
type lnSyncPlan struct {
	Create []taggedMove `json:"create"`
	Update []linkUpdate `json:"update"`
	Prune  []linkPrune  `json:"prune"`
}

// lnSyncApply confirms and carries out a synchronization with pruning or
// metadata updates: stale symlinks go to the trash first, then the missing
// ones are created and the others updated.
func (a *App) lnSyncApply(cmd *cobra.Command, f lnFlags, req api.CreateSymlinkRequest, plan []taggedMove, prune []linkPrune, updates []linkUpdate) error {
	if len(plan) == 0 && len(prune) == 0 && len(updates) == 0 {
		a.printer.Infof("Nothing to do.")
		if a.jsonOut && !f.dryRun {
			return a.printer.JSONValue(lnSyncResult{Created: []*api.Object{}, Updated: []*api.Object{}, Pruned: []string{}})
		}
		return nil
	}
	var lines []string
	for _, m := range plan {
		lines = append(lines, "+ "+m.To+" -> "+m.From)
	}
	for _, u := range updates {
		lines = append(lines, "~ "+u.Path+" ("+strings.Join(u.Fields, ", ")+")")
	}
	for _, p := range prune {
		lines = append(lines, "- "+p.Path+" ("+p.Reason+")")
	}
	q := fmt.Sprintf("Create %d symlink(s), update %d and move %d to the trash?", len(plan), len(updates), len(prune))
	if ok, err := a.confirmPlan(lines, lnSyncPlan{Create: nonNil(plan), Update: nonNil(updates), Prune: nonNil(prune)}, q, f.yes, f.dryRun); err != nil || !ok {
		return err
	}
	c, _, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	res := lnSyncResult{Created: []*api.Object{}, Updated: []*api.Object{}, Pruned: []string{}}
	var errs []error
	var paths []string
	for _, p := range prune {
		_, err := c.Delete(ctx, p.Path, client.DeleteOptions{})
		errs, paths = append(errs, err), append(paths, p.Path)
		if err != nil {
			continue
		}
		res.Pruned = append(res.Pruned, p.Path)
		a.printer.Printf("Moved %s to the trash (%s)\n", p.Path, p.Reason)
	}
	for _, m := range plan {
		r := req
		r.Source, r.Path = m.From, m.To
		o, _, err := c.CreateSymlink(ctx, r)
		if err != nil {
			err = fmt.Errorf("%s: %w", m.From, a.symlinkError(ctx, c, err, m.From))
		}
		errs, paths = append(errs, err), append(paths, m.From)
		if err != nil {
			continue
		}
		res.Created = append(res.Created, o)
		a.printSymlink(o)
	}
	for _, u := range updates {
		o, _, err := c.CreateSymlink(ctx, u.req)
		if err != nil {
			err = fmt.Errorf("%s: %w", u.Path, a.symlinkError(ctx, c, err, u.Source))
		}
		errs, paths = append(errs, err), append(paths, u.Path)
		if err != nil {
			continue
		}
		res.Updated = append(res.Updated, o)
		a.printer.Printf("Updated %s (%s)\n", u.Path, strings.Join(u.Fields, ", "))
	}
	if err := batchResult("sync", len(errs), errs, paths); err != nil {
		return err
	}
	if a.jsonOut {
		return a.printer.JSONValue(res)
	}
	return nil
}

// nonNil turns a nil slice into an empty one, so that JSON shows [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
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
			if dest != source && api.IsWithin(dest, source) && api.IsWithin(o.Path, dest) {
				continue // the server leaves a nested destination out too
			}
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
