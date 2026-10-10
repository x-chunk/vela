package cli

import (
	"time"

	"github.com/spf13/cobra"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/cli/config"
)

// metaFlags are the metadata options shared by put and new.
type metaFlags struct {
	public, unlisted, private bool
	title, description        string
	tags                      []string
	contentType, disposition  string
	expires                   string
}

func (m *metaFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&m.public, "public", false, "listed in the index and served by URL")
	f.BoolVar(&m.unlisted, "unlisted", false, "served by URL but not listed (not access control!)")
	f.BoolVar(&m.private, "private", false, "only via API key or signed link (vela share)")
	cmd.MarkFlagsMutuallyExclusive("public", "unlisted", "private")
	f.StringVar(&m.title, "title", "", "title shown in the index")
	f.StringVar(&m.description, "description", "", "description shown in the index")
	f.StringArrayVar(&m.tags, "tag", nil, "tag (repeatable)")
	f.StringVar(&m.contentType, "content-type", "", "MIME type (default: detected by the server)")
	f.StringVar(&m.disposition, "disposition", "", "inline or attachment (download)")
	f.StringVar(&m.expires, "expires", "", "delete automatically after a duration (7d) or at an RFC 3339 time")
}

// meta is the parsed form of metaFlags.
type meta struct {
	visibility  api.Visibility
	title       *string
	description *string
	tags        []string
	contentType string
	disposition api.Disposition
	expiresAt   *time.Time
}

func (m *metaFlags) parse(cmd *cobra.Command, s config.Settings, now time.Time) (meta, error) {
	var out meta
	switch {
	case m.public:
		out.visibility = api.Public
	case m.unlisted:
		out.visibility = api.Unlisted
	case m.private:
		out.visibility = api.Private
	default:
		out.visibility = s.DefaultVisibility // may be empty: server default
	}
	if cmd.Flags().Changed("title") {
		if err := api.ValidateTitle(m.title); err != nil {
			return out, usageError(err)
		}
		out.title = &m.title
	}
	if cmd.Flags().Changed("description") {
		if err := api.ValidateDescription(m.description); err != nil {
			return out, usageError(err)
		}
		out.description = &m.description
	}
	if len(m.tags) > 0 {
		tags, err := api.NormalizeTags(m.tags)
		if err != nil {
			return out, usageError(err)
		}
		out.tags = tags
	}
	out.contentType = m.contentType
	if m.disposition != "" {
		d, err := api.ParseDisposition(m.disposition)
		if err != nil {
			return out, usageError(err)
		}
		out.disposition = d
	}
	if m.expires != "" {
		t, err := api.ParseExpiry(m.expires, now)
		if err != nil {
			return out, usageError(err)
		}
		if t == nil {
			return out, usagef("--expires none makes no sense for a new upload; omit the flag")
		}
		out.expiresAt = t
	}
	return out, nil
}
