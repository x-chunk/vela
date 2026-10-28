package cli

import (
	"context"
	"fmt"
	"strings"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/output"
)

// isSymlink reports whether o is a symlink. Objects from servers without
// symlink support have no kind and are files.
func isSymlink(o *api.Object) bool { return o.Kind == api.KindSymlink }

// isBroken reports whether o is a symlink whose target this key cannot
// follow: trashed, purged, or outside the key's prefix.
func isBroken(o *api.Object) bool {
	return isSymlink(o) && (o.Target == nil || o.Target.State != api.TargetOK)
}

// targetLabel describes where a symlink points, for listings.
func targetLabel(o *api.Object) string {
	if o.Target == nil || o.Target.Path == nil {
		return "?"
	}
	return *o.Target.Path
}

// brokenLabel is the "(broken: ...)" suffix of a broken symlink.
func brokenLabel(o *api.Object) string {
	state := api.TargetGone
	if o.Target != nil {
		state = o.Target.State
	}
	return fmt.Sprintf("(broken: %s)", state)
}

// keepObjects filters a listing on the client as well: a server without
// symlink support ignores the kind, broken and target parameters and would
// otherwise return every object, which rm --broken must never delete.
func keepObjects(objs []api.Object, kind api.ObjectKind, broken, target bool) []api.Object {
	if kind == "" && !broken && !target {
		return objs
	}
	out := []api.Object{}
	for i := range objs {
		o := &objs[i]
		switch {
		case kind == api.KindFile && isSymlink(o),
			(kind == api.KindSymlink || broken || target) && !isSymlink(o),
			broken && !isBroken(o):
			continue
		}
		out = append(out, *o)
	}
	return out
}

// maxListedSymlinks caps the symlink paths stat prints for a file.
const maxListedSymlinks = 10

// symlinksTo lists the symlinks within the key's prefix that point at the
// file o. It returns nil for servers without symlink support.
func symlinksTo(ctx context.Context, c *client.Client, o *api.Object) ([]api.Object, error) {
	if isSymlink(o) {
		return nil, nil
	}
	var out []api.Object
	opts := client.ListOptions{Recursive: true, Target: o.Path, Limit: 1000}
	for {
		page, err := c.List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, l := range page.Objects {
			// An older server ignores the target filter; its listing is
			// recognizable because nothing in it refers to o.
			if !isSymlink(&l) || l.Target == nil || l.Target.ID != o.ID {
				return nil, nil
			}
			out = append(out, l)
		}
		if page.NextCursor == "" {
			return out, nil
		}
		opts.Cursor = page.NextCursor
	}
}

// symlinkDetails are the stat lines specific to symlinks: where a symlink
// points, or which symlinks point at a file.
func (a *App) symlinkDetails(o *api.Object, links []api.Object) []output.KV {
	p := a.printer
	if !isSymlink(o) {
		if len(links) == 0 {
			return nil
		}
		lines := []string{fmt.Sprint(len(links))}
		for i, l := range links {
			if i == maxListedSymlinks {
				lines = append(lines, fmt.Sprintf("… and %d more (vela ls -r --target %s)", len(links)-i, o.Path))
				break
			}
			lines = append(lines, l.Path)
		}
		return []output.KV{{Key: "Symlinks", Value: strings.Join(lines, "\n")}}
	}
	target := targetLabel(o)
	if isBroken(o) {
		target += " " + string(p.Error(brokenLabel(o)))
	}
	kv := []output.KV{
		{Key: "Kind", Value: "symlink"},
		{Key: "Target", Value: output.Styled(target)},
	}
	if o.Target != nil && o.Target.Visibility != nil {
		kv = append(kv, output.KV{Key: "Target vis.", Value: output.Styled(string(p.Visibility(*o.Target.Visibility)))})
	}
	kv = append(kv, output.KV{Key: "Effective", Value: output.Styled(string(p.Visibility(o.EffectiveVisibility)))})
	return kv
}

// brokenHint suggests how to repair a broken symlink.
func brokenHint(o *api.Object) string {
	if o.Target != nil && o.Target.State == api.TargetTrashed && o.Target.Path != nil {
		return fmt.Sprintf("its target is in the trash; restore it with: vela trash restore %s", *o.Target.Path)
	}
	return fmt.Sprintf("its target is gone; remove it with \"vela rm %s\" or point it elsewhere with \"vela ln -f SOURCE %s\"", o.Path, o.Path)
}
