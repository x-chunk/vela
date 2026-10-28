package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
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
