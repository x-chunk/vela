package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

func TestTransferProgressModes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	content := strings.Repeat("x", progressThreshold+1)
	local := h.file("large.bin", content)
	h.seed("large.bin", content, api.Private)
	for _, tc := range []struct {
		name     string
		flags    []string
		env      map[string]string
		terminal bool
		bar      bool
		color    bool
	}{
		{name: "stderr terminal", terminal: true, bar: true, color: true},
		{name: "no-color flag", terminal: true, flags: []string{"--no-color"}, bar: true},
		{name: "NO_COLOR", terminal: true, env: map[string]string{"NO_COLOR": ""}, bar: true},
		{name: "pipe"},
		{name: "dumb terminal", terminal: true, env: map[string]string{"TERM": "dumb"}},
		{name: "quiet", terminal: true, flags: []string{"--quiet"}},
		{name: "json", terminal: true, flags: []string{"--json"}},
		{name: "verbose", terminal: true, flags: []string{"--verbose"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range [][]string{
				{"put", local, "uploaded.bin", "--force"},
				{"get", "large.bin", filepath.Join(t.TempDir(), "download.bin")},
			} {
				var out, errOut bytes.Buffer
				a := NewApp()
				a.Out, a.Err = &out, &errOut
				// stdout is redirected: progress color must depend on stderr.
				a.OutIsTerminal = func() bool { return false }
				a.ErrIsTerminal = func() bool { return tc.terminal }
				a.LookupEnv = func(k string) (string, bool) {
					if k == "VELA_CONFIG" {
						return h.config, true
					}
					v, ok := tc.env[k]
					return v, ok
				}
				if code := a.Execute(context.Background(), append(args, tc.flags...)); code != 0 {
					t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
				}
				got := errOut.String()
				if strings.Contains(got, "\r\x1b[2K") != tc.bar || strings.Contains(got, "\x1b[32m") != tc.color {
					t.Fatalf("%v: unexpected progress/style: %q", args, got)
				}
				if tc.bar && strings.Contains(got[:strings.LastIndex(got, "\r\x1b[2K")], "\n") {
					t.Fatalf("%v: progress printed multiple lines: %q", args, got)
				}
				if strings.Contains(out.String(), "\x1b[") {
					t.Fatalf("%v: progress leaked to stdout: %q", args, out.String())
				}
			}
		})
	}
}
