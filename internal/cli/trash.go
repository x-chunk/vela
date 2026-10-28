package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/output"
	"go.xchunk.org/vela/internal/ids"
)

func (a *App) trashCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "trash", Short: "List, restore or purge deleted files"}
	cmd.AddCommand(a.trashLsCommand(), a.trashRestoreCommand(), a.trashPurgeCommand())
	return cmd
}

func (a *App) trashLsCommand() *cobra.Command {
	var byTag tagFlags
	cmd := &cobra.Command{
		Use:   "ls [DIR/]",
		Short: "List the trash, newest first",
		Long: "List the trash, newest first. --by-tag and --by-tags list only entries whose file had\n" +
			"these tags when it was deleted.",
		Args: rangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tags, err := byTag.filter()
			if err != nil {
				return err
			}
			prefix := ""
			if len(args) == 1 {
				if prefix, err = remotePrefix(args[0]); err != nil {
					return err
				}
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			entries, err := c.ListAllTrash(ctx, client.TrashOptions{Prefix: prefix, Tags: tags, Limit: 1000})
			if err != nil {
				return err
			}
			if a.jsonOut {
				if entries == nil {
					entries = []api.TrashEntry{}
				}
				return a.printer.JSONValue(api.TrashList{Entries: entries})
			}
			if len(entries) == 0 {
				a.printer.Infof("The trash is empty.")
				return nil
			}
			rows := make([][]any, 0, len(entries))
			for _, e := range entries {
				path := output.Clean(e.OriginalPath)
				if isSymlink(&e.Object) {
					path += " -> " + output.Clean(targetLabel(&e.Object))
				}
				rows = append(rows, []any{e.ID, output.Time(e.DeletedAt), output.Time(e.PurgeAt), output.Size(e.Object.Size), path})
			}
			a.printer.Table([]string{"ID", "DELETED", "PURGED AT", "SIZE", "PATH"}, rows)
			return nil
		},
	}
	byTag.register(cmd)
	return cmd
}

func (a *App) trashRestoreCommand() *cobra.Command {
	var to string
	var force bool
	cmd := &cobra.Command{
		Use:   "restore ID|ORIGINAL_PATH",
		Short: "Restore a deleted file",
		Long: "Restore a trash entry by its ID, or by its original path (the most recently deleted\n" +
			"entry with that path). Signed links to the file work again.",
		Example: "  vela trash restore notes/old.md\n  vela trash restore 01jb6z... --to notes/recovered.md",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			id := args[0]
			if !ids.Valid(strings.ToLower(id)) {
				path, err := remotePath(id)
				if err != nil {
					return err
				}
				list, err := c.ListTrash(ctx, client.TrashOptions{Path: path, Limit: 1})
				if err != nil {
					return err
				}
				if len(list.Entries) == 0 {
					return &cliError{code: exitNotFound, slug: "not_found", msg: "nothing in the trash with path " + path,
						hint: "see \"vela trash ls\""}
				}
				id = list.Entries[0].ID
			}
			req := api.RestoreRequest{Overwrite: force}
			if to != "" {
				if req.Path, err = remotePath(to); err != nil {
					return err
				}
			}
			o, err := c.Restore(ctx, strings.ToLower(id), req)
			if err != nil {
				return hintFor(err, api.CodeAlreadyExists, "restore elsewhere with --to, or overwrite with --force")
			}
			if a.jsonOut {
				return a.printer.JSONValue(o)
			}
			a.printer.Printf("Restored %s\n", o.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "restore to this path instead of the original")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite a file at the target path (it goes to the trash)")
	return cmd
}

func (a *App) trashPurgeCommand() *cobra.Command {
	var olderThan string
	var yes bool
	cmd := &cobra.Command{
		Use:   "purge [ID...]",
		Short: "Permanently delete trash entries (admin)",
		Long: "Permanently delete the given trash entries, or all entries (optionally only those older\n" +
			"than --older-than). This cannot be undone and asks for confirmation unless -y is given.",
		Example: "  vela trash purge --older-than 7d -y\n  vela trash purge 01jb6z...",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && olderThan != "" {
				return usagef("give either IDs or --older-than, not both")
			}
			if olderThan != "" {
				if _, err := api.ParseDuration(olderThan); err != nil {
					return usageError(err)
				}
			}
			q := "Permanently delete everything in the trash?"
			switch {
			case len(args) > 0:
				q = fmt.Sprintf("Permanently delete %d trash entr%s?", len(args), map[bool]string{true: "y", false: "ies"}[len(args) == 1])
			case olderThan != "":
				q = "Permanently delete trash entries older than " + olderThan + "?"
			}
			if err := a.confirm(q, yes); err != nil {
				return err
			}
			c, _, err := a.client()
			if err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			if len(args) == 0 {
				res, err := c.PurgeTrash(ctx, olderThan)
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.printer.JSONValue(res)
				}
				a.printer.Printf("Purged %d trash entr%s.\n", res.Purged, map[bool]string{true: "y", false: "ies"}[res.Purged == 1])
				a.warnSymlinksAffected(res.SymlinksAffected, true)
				return nil
			}
			errs := make([]error, len(args))
			for i, id := range args {
				errs[i] = c.PurgeTrashEntry(ctx, strings.ToLower(id))
			}
			if err := batchResult("purge", len(args), errs, args); err != nil {
				return err
			}
			if a.jsonOut {
				return a.printer.JSONValue(api.PurgeResponse{Purged: len(args)})
			}
			a.printer.Printf("Purged %d trash entr%s.\n", len(args), map[bool]string{true: "y", false: "ies"}[len(args) == 1])
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "only entries deleted longer ago than this, e.g. 7d")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
