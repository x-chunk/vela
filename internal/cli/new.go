package cli

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
)

func (a *App) newCommand() *cobra.Command {
	var (
		m       metaFlags
		content string
		stdin   bool
		edit    bool
	)
	cmd := &cobra.Command{
		Use:   "new REMOTE [--content TEXT | --stdin | --edit]",
		Short: "Create a small file from text, stdin or an editor",
		Long: "Create a new file from raw content: a string, stdin, or $VISUAL/$EDITOR (vi by default).\n" +
			"An empty editor buffer cancels. Never overwrites; for large files use \"vela put\".",
		Example: "  vela new notes/todo.md --content '# Todo'\n" +
			"  echo hello | vela new hello.txt --stdin --public\n" +
			"  vela new notes/idea.md --edit",
		Args:              exactArgs(1),
		ValidArgsFunction: a.completeRemote(),
		RunE: func(cmd *cobra.Command, args []string) error {
			remote, err := remotePath(args[0])
			if err != nil {
				return err
			}
			sources := 0
			for _, b := range []bool{cmd.Flags().Changed("content"), stdin, edit} {
				if b {
					sources++
				}
			}
			if sources > 1 {
				return usagef("use only one of --content, --stdin and --edit")
			}
			if sources == 0 {
				// Piped input is the natural source; otherwise open an editor.
				if a.InIsTerminal() {
					edit = true
				} else {
					stdin = true
				}
			}
			var data []byte
			switch {
			case stdin:
				if data, err = io.ReadAll(a.In); err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
			case edit:
				if data, err = a.editContent(cmd, remote); err != nil {
					return err
				}
				if strings.TrimSpace(string(data)) == "" {
					return &cliError{code: exitError, slug: "aborted", msg: "aborted: the file is empty, nothing was created"}
				}
			default:
				data = []byte(content)
			}

			c, s, err := a.client()
			if err != nil {
				return err
			}
			md, err := m.parse(cmd, s, a.Now())
			if err != nil {
				return err
			}
			req := api.CreateObjectRequest{
				Path:        remote,
				ContentType: md.contentType,
				Visibility:  md.visibility,
				Tags:        md.tags,
				Disposition: md.disposition,
				ExpiresAt:   md.expiresAt,
			}
			if md.title != nil {
				req.Title = *md.title
			}
			if md.description != nil {
				req.Description = *md.description
			}
			if utf8.Valid(data) {
				req.Content, req.Encoding = string(data), api.EncodingUTF8
			} else {
				req.Content, req.Encoding = base64.StdEncoding.EncodeToString(data), api.EncodingBase64
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			o, err := c.Create(ctx, req)
			if err != nil {
				err = hintFor(err, api.CodeAlreadyExists, "\"vela new\" never overwrites; use \"vela put --force\"")
				return hintFor(err, api.CodePayloadTooLarge, "use \"vela put\" for larger files")
			}
			if a.jsonOut {
				return a.printer.JSONValue(o)
			}
			a.printResultURL(*o)
			return nil
		},
	}
	m.register(cmd)
	cmd.Flags().StringVar(&content, "content", "", "file content")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the content from stdin")
	cmd.Flags().BoolVar(&edit, "edit", false, "write the content in $VISUAL or $EDITOR")
	return cmd
}

// editContent opens the user's editor on a temporary file. The file lives
// in a private temporary directory and is named after the remote file when
// that name is short, so the editor picks the right syntax highlighting; a
// long name is replaced by "content" with the original extension, because
// prefixes or suffixes added to a name at the filesystem's limit would make
// it too long.
func (a *App) editContent(cmd *cobra.Command, remote string) ([]byte, error) {
	editor := strings.TrimSpace(a.env("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(a.env("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}
	dir, err := os.MkdirTemp("", "vela-edit-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	name := filepath.Join(dir, editName(path.Base(remote)))
	if err := os.WriteFile(name, nil, 0o600); err != nil {
		return nil, err
	}
	// $EDITOR may carry arguments, as in "code --wait".
	parts := strings.Fields(editor)
	if err := a.Run(cmd.Context(), nil, true, parts[0], append(parts[1:], name)...); err != nil {
		return nil, fmt.Errorf("run editor %q: %w", editor, err)
	}
	return os.ReadFile(name) // #nosec G304 -- our own temporary file
}

// editName picks the editor's file name for a remote base name.
func editName(base string) string {
	const maxName, maxExt = 64, 16
	if len(base) <= maxName {
		return base
	}
	if ext := path.Ext(base); len(ext) <= maxExt {
		return "content" + ext
	}
	return "content"
}
