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

// tagFlags are --by-tag and --by-tags, which select objects by their tags
// instead of naming them. Each --by-tag is one tag and each --by-tags a
// space-separated list of tags that must all be present; an object matching
// any of them is selected.
type tagFlags struct {
	any, all []string
}

func (t *tagFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&t.any, "by-tag", nil, "select objects with this tag (repeatable; any of them matches)")
	f.StringArrayVar(&t.all, "by-tags", nil, "select objects with all of these space-separated tags (repeatable; any list matches)")
}

// filter validates the flags; a bad tag is a usage error before any request.
func (t *tagFlags) filter() (api.TagFilter, error) {
	var f api.TagFilter
	for _, raw := range t.any {
		g, err := api.NormalizeTags([]string{raw})
		if err != nil {
			return nil, usageError(fmt.Errorf("--by-tag: %w (use --by-tags for several tags)", err))
		}
		f = f.Or(g)
	}
	for _, raw := range t.all {
		g, err := api.ParseTagGroup(raw)
		if err != nil {
			return nil, usageError(fmt.Errorf("--by-tags: %w", err))
		}
		f = f.Or(g)
	}
	if len(f) > api.MaxTagGroups {
		return nil, usagef("too many tag selections: %d (max %d)", len(f), api.MaxTagGroups)
	}
	return f, nil
}

// selectTagged returns the objects matching f, sorted by path. Each scope is
// a file, kept if it matches, or a directory searched recursively; no scope
// searches everything the key can see.
func (a *App) selectTagged(ctx context.Context, c *client.Client, scopes []string, f api.TagFilter) ([]api.Object, error) {
	if len(scopes) == 0 {
		scopes = []string{""}
	}
	var out []api.Object
	for _, raw := range scopes {
		raw = strings.TrimLeft(raw, "/")
		if raw != "" && !strings.HasSuffix(raw, "/") {
			p, err := remotePath(raw)
			if err != nil {
				return nil, err
			}
			o, err := c.Stat(ctx, p)
			if err == nil {
				if f.Match(o.Tags) {
					out = append(out, *o)
				}
				continue
			}
			if !api.IsCode(err, api.CodeNotFound) {
				return nil, err
			}
			raw += "/"
		}
		prefix, err := remotePrefix(raw)
		if err != nil {
			return nil, err
		}
		l, err := c.ListAll(ctx, client.ListOptions{Prefix: prefix, Recursive: true, Tags: f, Limit: 1000})
		if err != nil {
			return nil, err
		}
		out = append(out, l.Objects...)
	}
	slices.SortStableFunc(out, func(x, y api.Object) int { return strings.Compare(x.Path, y.Path) })
	return uniqueObjects(out), nil
}

// errNothingTagged is the error of commands that act on a tag selection
// which matched nothing.
func errNothingTagged(f api.TagFilter) error {
	return &cliError{code: exitNotFound, slug: string(api.CodeNotFound), msg: "nothing tagged " + f.String()}
}

func (t *tagFlags) used() bool { return len(t.any) > 0 || len(t.all) > 0 }

// args checks positional arguments with normal unless a tag selection is
// given, in which case any number of scopes (including none) is fine.
func (t *tagFlags) args(normal cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if t.used() {
			return nil
		}
		return normal(cmd, args)
	}
}
