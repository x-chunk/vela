package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/client"
)

// completionTimeout bounds remote completion: a slow or unreachable server
// must not freeze the shell.
const completionTimeout = 1500 * time.Millisecond

// completeRemote completes remote paths by listing the directory typed so
// far. Directories end with "/" and keep the cursor on the word so the user
// can descend. Any error yields no suggestions and no output, because noise
// on a terminal during completion is worse than no completion.
func (a *App) completeRemote() cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		none := cobra.ShellCompDirectiveNoFileComp
		c, _, err := a.client()
		if err != nil {
			return nil, none
		}
		parent := cmd.Context()
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, completionTimeout)
		defer cancel()
		typed := strings.TrimLeft(toComplete, "/")
		prefix := ""
		if i := strings.LastIndexByte(typed, '/'); i >= 0 {
			prefix = typed[:i+1]
		}
		l, err := c.List(ctx, client.ListOptions{Prefix: prefix, Limit: 1000})
		if err != nil {
			return nil, none
		}
		var out []string
		for _, d := range l.Dirs {
			if strings.HasPrefix(d, typed) {
				out = append(out, d)
			}
		}
		for _, o := range l.Objects {
			if strings.HasPrefix(o.Path, typed) {
				out = append(out, o.Path)
			}
		}
		directive := none
		for _, s := range out {
			if strings.HasSuffix(s, "/") {
				directive |= cobra.ShellCompDirectiveNoSpace
				break
			}
		}
		return out, directive
	}
}
