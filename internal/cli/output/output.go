// Package output formats what the vela CLI prints: human-readable tables and
// messages, or stable JSON for scripts (the DTOs from package api).
//
// Conventions: results go to stdout, progress and diagnostics to stderr. In
// JSON mode stdout carries exactly one JSON document, including for errors,
// so that scripts can always parse it. Colors are used only on terminals and
// honor NO_COLOR.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/units"
)

// Printer writes command output.
type Printer struct {
	Out   io.Writer
	Err   io.Writer
	JSON  bool
	Quiet bool
	Color bool
	// Now is used for relative times; nil means time.Now.
	Now func() time.Time
}

// JSONValue writes v as indented JSON to stdout.
func (p *Printer) JSONValue(v any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// All printing methods treat their arguments as data and clean them (see
// Clean), except Styled values, which the CLI produced itself.

// Println writes a result line to stdout (human mode only).
func (p *Printer) Println(a ...any) {
	if !p.JSON {
		_, _ = fmt.Fprintln(p.Out, texts(a)...)
	}
}

// Printf writes a formatted result to stdout (human mode only).
func (p *Printer) Printf(format string, a ...any) {
	if !p.JSON {
		_, _ = fmt.Fprintf(p.Out, format, texts(a)...)
	}
}

// Infof writes a diagnostic to stderr unless quiet.
func (p *Printer) Infof(format string, a ...any) {
	if !p.Quiet {
		_, _ = fmt.Fprintf(p.Err, format+"\n", texts(a)...)
	}
}

// Warnf writes a warning to stderr, even when quiet.
func (p *Printer) Warnf(format string, a ...any) {
	_, _ = fmt.Fprintf(p.Err, "%s "+format+"\n", append([]any{p.paint(yellow, "warning:")}, texts(a)...)...)
}

// Errorf writes an error line to stderr.
func (p *Printer) Errorf(label, format string, a ...any) {
	_, _ = fmt.Fprintf(p.Err, "%s "+format+"\n", append([]any{p.Error(label)}, texts(a)...)...)
}

// Table prints aligned columns to stdout. Cells are strings (data, cleaned)
// or Styled.
func (p *Printer) Table(header []string, rows [][]any) {
	if p.JSON {
		return
	}
	tw := tabwriter.NewWriter(p.Out, 0, 0, 2, ' ', 0)
	if header != nil {
		cells := make([]string, len(header))
		for i, h := range header {
			cells[i] = string(p.paint(bold, h))
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = text(c)
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
}

// KV is one line of KeyValues; Value is a string (data) or Styled.
type KV struct {
	Key   string
	Value any
}

// KeyValues prints "key: value" lines with aligned values. A value with line
// breaks continues on indented lines.
func (p *Printer) KeyValues(pairs []KV) {
	if p.JSON {
		return
	}
	tw := tabwriter.NewWriter(p.Out, 0, 0, 2, ' ', 0)
	for _, kv := range pairs {
		v := text(kv.Value)
		if s, ok := kv.Value.(string); ok {
			v = CleanMultiline(s)
		}
		lines := strings.Split(v, "\n")
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", p.paint(dim, kv.Key+":"), lines[0])
		for _, l := range lines[1:] {
			_, _ = fmt.Fprintf(tw, "\t%s\n", l)
		}
	}
	_ = tw.Flush()
}

// ANSI styles.
const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	blue   = "\x1b[34m"
	purple = "\x1b[35m"
)

// paint styles s, cleaning it first: s may be data, such as a directory
// name.
func (p *Printer) paint(style, s string) Styled {
	s = Clean(s)
	if !p.Color || s == "" {
		return Styled(s)
	}
	return Styled(style + s + reset)
}

// Dir styles a directory name.
func (p *Printer) Dir(s string) Styled { return p.paint(blue+bold, s) }

// Dim styles secondary text.
func (p *Printer) Dim(s string) Styled { return p.paint(dim, s) }

// Error styles an error label.
func (p *Printer) Error(s string) Styled { return p.paint(red+bold, s) }

// Visibility renders a visibility with a color and a fixed-width label, so
// it stays readable without color too.
func (p *Printer) Visibility(v api.Visibility) Styled {
	switch v {
	case api.Public:
		return p.paint(green, "public  ")
	case api.Unlisted:
		return p.paint(yellow, "unlisted")
	case api.Private:
		return p.paint(purple, "private ")
	default:
		return Styled(Clean(string(v)))
	}
}

// Size renders a byte count for humans.
func Size(n int64) string { return units.FormatSize(n) }

// Time renders a timestamp in the local time zone.
func Time(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// TimePtr renders an optional timestamp.
func TimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return Time(*t)
}

// Remaining renders the time until t, coarsely: "3d 4h", "2h 5m", "45s",
// or "expired".
func (p *Printer) Remaining(t time.Time) string {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	return Duration(t.Sub(now))
}

// Duration renders a positive duration coarsely, or "expired".
func Duration(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	s := (d - m*time.Minute) / time.Second
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
