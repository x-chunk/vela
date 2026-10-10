package output

import (
	"fmt"
	"strings"
	"unicode"
)

// Styled is text produced by the CLI itself, possibly with ANSI styling; it
// is printed as is. Everything else that reaches a terminal (paths, titles,
// notes, server messages) is data, which a key holder with write access
// controls, and goes through Clean.
type Styled string

// Clean makes data safe to print on a terminal. Control characters (C0,
// DEL, C1, including ESC, BEL, CR and backspace) could otherwise clear or
// rewrite the screen or, through OSC sequences, set the clipboard or the
// window title; bidirectional overrides could make text read differently
// from what it is. They are shown as visible escapes (\x1b, ‮) instead.
// The stored values and JSON output are untouched.
func Clean(s string) string { return clean(s, false) }

// CleanMultiline is Clean but keeps line feeds, for values shown on several
// lines.
func CleanMultiline(s string) string { return clean(s, true) }

func clean(s string, keepNewlines bool) string {
	if !needsCleaning(s, keepNewlines) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' && keepNewlines:
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case unicode.IsControl(r) && r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.IsControl(r) || isBidiControl(r) || r == '�':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsCleaning(s string, keepNewlines bool) bool {
	for _, r := range s {
		if (unicode.IsControl(r) && !(keepNewlines && r == '\n')) || isBidiControl(r) || r == '�' {
			return true
		}
	}
	return false
}

// isBidiControl reports the invisible characters that change text direction.
func isBidiControl(r rune) bool {
	return r == '؜' || r == '‎' || r == '‏' || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩')
}

// text renders a value for terminal output: Styled as is, anything else
// cleaned.
func text(v any) string {
	switch t := v.(type) {
	case Styled:
		return string(t)
	case string:
		return Clean(t)
	case error:
		return Clean(t.Error())
	default:
		return Clean(fmt.Sprint(t))
	}
}

func texts(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch a.(type) {
		case Styled, string, error, fmt.Stringer:
			out[i] = text(a)
		default:
			out[i] = a // numbers and the like keep their verbs
		}
	}
	return out
}
