package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/output"
)

const hiddenDirectoryWarning = "Hidden directories are not access control. Anyone who knows or guesses the URL can see the index. Use a hard-to-guess name or private files with signed links for anything sensitive."

func (a *App) dirCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "dir", Short: "Manage directory index and crawler policies", Long: "Manage directory policies independently of files. Prefixes accept an optional trailing slash; / is the root.\n\n" + hiddenDirectoryWarning}
	cmd.AddCommand(a.dirListCommand(), a.dirShowCommand(), a.dirSetCommand(), a.dirRemoveCommand(), a.dirHideCommand(), a.dirPrivateRootCommand())
	return cmd
}

func (a *App) dirListCommand() *cobra.Command {
	var orphaned bool
	cmd := &cobra.Command{
		Use: "ls [PREFIX]", Short: "List explicit directory policies", Args: rangeArgs(0, 1), ValidArgsFunction: a.completeDirectories(),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) != 0 {
				var err error
				prefix, err = remotePrefix(args[0])
				if err != nil {
					return err
				}
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			list, err := c.ListAllDirPolicies(ctx, client.DirListOptions{Prefix: prefix, Recursive: true, Orphaned: orphaned, Limit: 1000})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(list)
			}
			rows := make([][]any, 0, len(list.Policies))
			for _, rule := range list.Policies {
				dir, err := c.ShowDir(ctx, rule.Prefix)
				if err != nil {
					return err
				}
				rows = append(rows, []any{displayPrefix(rule.Prefix), a.dirMode(rule.Index), rule.Hidden, a.dirMode(rule.NoIndex), rule.Title, dir.Exists})
			}
			a.printer.Table([]string{"PREFIX", "INDEX", "HIDDEN", "NOINDEX", "TITLE", "EXISTS"}, rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&orphaned, "orphaned", false, "only rules with no live public objects below them")
	return cmd
}

func displayPrefix(prefix string) string {
	if prefix == "" {
		return "/"
	}
	return prefix
}

func (a *App) dirMode(mode api.IndexMode) any {
	if mode == api.IndexInherit {
		return a.printer.Dim("(inherit)")
	}
	return string(mode)
}

func (a *App) dirShowCommand() *cobra.Command {
	return &cobra.Command{
		Use: "show PREFIX", Short: "Show effective settings and what an anonymous visitor sees", Args: exactArgs(1), ValidArgsFunction: a.completeDirectories(),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix, err := remotePrefix(args[0])
			if err != nil {
				return err
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			dir, err := c.ShowDir(ctx, prefix)
			if err != nil {
				return err
			}
			return a.printDir(dir)
		},
	}
}

func (a *App) printDir(dir *api.EffectiveDir) error {
	if a.jsonOut {
		return a.printer.JSONValue(dir)
	}
	effective := dir.Effective
	a.printer.KeyValues([]output.KV{
		{Key: "Prefix", Value: displayPrefix(dir.Prefix)},
		{Key: "Index", Value: fmt.Sprintf("%s (source: %s)", effective.Index, displayPrefix(effective.IndexSource))},
		{Key: "Hidden", Value: effective.Hidden},
		{Key: "Noindex", Value: fmt.Sprintf("%s (source: %s)", effective.NoIndex, displayPrefix(effective.NoIndexSource))},
		{Key: "Title", Value: dashIfEmpty(effective.Title)},
		{Key: "Description", Value: dashIfEmpty(effective.Description)},
		{Key: "Exists", Value: dir.Exists},
	})
	if dir.URL == nil {
		why := "index off"
		if effective.Index == api.IndexOn {
			why = "no live public objects"
		}
		a.printer.Printf("Index page: disabled (404; %s)\n", why)
		return nil
	}
	var notes []string
	if dir.Prefix != "" && !dir.ListedInParent {
		notes = append(notes, "not listed in parent")
	}
	if effective.NoIndex == api.IndexOn {
		notes = append(notes, "hidden from search engines")
	}
	suffix := ""
	if len(notes) != 0 {
		suffix = " (" + strings.Join(notes, ", ") + ")"
	}
	a.printer.Printf("Index page: %s%s\n", *dir.URL, suffix)
	return nil
}

func (a *App) dirSetCommand() *cobra.Command {
	var index, noindex, title, description string
	var hidden, noHidden bool
	cmd := &cobra.Command{Use: "set PREFIX", Short: "Patch an explicit directory policy", Args: exactArgs(1), ValidArgsFunction: a.completeDirectories()}
	cmd.Flags().StringVar(&index, "index", "", "index: on, off or inherit")
	cmd.Flags().StringVar(&noindex, "noindex", "", "crawler exclusion: on, off or inherit")
	cmd.Flags().BoolVar(&hidden, "hidden", false, "hide this directory in its parent index")
	cmd.Flags().BoolVar(&noHidden, "no-hidden", false, "show this directory in its parent index")
	cmd.MarkFlagsMutuallyExclusive("hidden", "no-hidden")
	cmd.Flags().StringVar(&title, "title", "", "plain-text index title")
	cmd.Flags().StringVar(&description, "description", "", "plain-text index description")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		patch := api.DirPolicyPatch{}
		for _, field := range []struct {
			name, value string
			dst         *api.Optional[api.IndexMode]
		}{{"index", index, &patch.Index}, {"noindex", noindex, &patch.NoIndex}} {
			if cmd.Flags().Changed(field.name) {
				mode := api.IndexMode(field.value)
				if !mode.Valid() {
					return usagef("%s must be on, off or inherit", field.name)
				}
				*field.dst = api.Some(mode)
			}
		}
		if cmd.Flags().Changed("hidden") {
			patch.Hidden = api.Some(hidden)
		}
		if cmd.Flags().Changed("no-hidden") {
			patch.Hidden = api.Some(!noHidden)
		}
		if cmd.Flags().Changed("title") {
			if err := api.ValidateTitle(title); err != nil {
				return usageError(err)
			}
			patch.Title = api.Some(title)
		}
		if cmd.Flags().Changed("description") {
			if err := api.ValidateDescription(description); err != nil {
				return usageError(err)
			}
			patch.Description = api.Some(description)
		}
		return a.patchDirCommand(cmd, args[0], patch)
	}
	modes := func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"on", "off", "inherit"}, cobra.ShellCompDirectiveNoFileComp
	}
	_ = cmd.RegisterFlagCompletionFunc("index", modes)
	_ = cmd.RegisterFlagCompletionFunc("noindex", modes)
	return cmd
}

func (a *App) patchDirCommand(cmd *cobra.Command, raw string, patch api.DirPolicyPatch) error {
	prefix, err := remotePrefix(raw)
	if err != nil {
		return err
	}
	if prefix == "" && patch.Hidden.HasValue() && patch.Hidden.Value {
		return usagef("the root directory cannot be hidden")
	}
	c, _, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	dir, err := c.PatchDir(ctx, prefix, patch, client.DirWriteOptions{})
	if err != nil {
		return err
	}
	return a.printDir(dir)
}

func (a *App) dirHideCommand() *cobra.Command {
	return &cobra.Command{Use: "hide PREFIX", Short: "Hide a directory's entrance while enabling its index", Long: "Set hidden=true and index=on. Descendant directories remain listed inside it.\n\n" + hiddenDirectoryWarning, Args: exactArgs(1), ValidArgsFunction: a.completeDirectories(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.patchDirCommand(cmd, args[0], api.DirPolicyPatch{Hidden: api.Some(true), Index: api.Some(api.IndexOn)})
		},
	}
}

func (a *App) dirPrivateRootCommand() *cobra.Command {
	return &cobra.Command{Use: "private-root", Short: "Disable the root index; public file URLs remain accessible", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.printer.Warnf("Public files remain accessible through their direct URLs; disabling the index is not access control.")
			return a.patchDirCommand(cmd, "/", api.DirPolicyPatch{Index: api.Some(api.IndexOff)})
		},
	}
}

func (a *App) dirRemoveCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "rm PREFIX", Short: "Remove an explicit policy and restore inheritance", Args: exactArgs(1), ValidArgsFunction: a.completeDirectories(),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix, err := remotePrefix(args[0])
			if err != nil {
				return err
			}
			if prefix == "" {
				if err := a.confirm("Remove the root directory policy?", yes); err != nil {
					return err
				}
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if err := c.DeleteDir(ctx, prefix, client.DirWriteOptions{}); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(struct {
					Prefix  string `json:"prefix"`
					Deleted bool   `json:"deleted"`
				}{prefix, true})
			}
			a.printer.Printf("Removed directory policy: %s\n", displayPrefix(prefix))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm removal of the root policy")
	return cmd
}

func (a *App) completeDirectories() cobra.CompletionFunc {
	complete := a.completeRemote()
	return func(cmd *cobra.Command, args []string, typed string) ([]string, cobra.ShellCompDirective) {
		paths, directive := complete(cmd, args, typed)
		out := []string{}
		for _, path := range paths {
			if strings.HasSuffix(path, "/") {
				out = append(out, path)
			}
		}
		if typed == "" || typed == "/" {
			out = append(out, "/")
		}
		return out, directive
	}
}
