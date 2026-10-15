package output

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProgressFitsTerminal(t *testing.T) {
	t.Parallel()
	styles := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	for _, label := range []string{"", strings.Repeat("long-path/", 10) + "file.bin", strings.Repeat("界😀", 30) + "报告.bin"} {
		for _, color := range []bool{false, true} {
			var buf bytes.Buffer
			p := NewProgress(&buf, label, 1<<60, color)
			columns := 80
			p.columns = func() int { return columns }
			p.done = p.total / 2
			// Changing the width between frames also exercises resize handling.
			for _, columns = range []int{120, 80, 73, 72, 60, 51, 50, 41, 40, 20, 10, 5, 1} {
				buf.Reset()
				p.draw(p.start.Add(time.Second))
				raw := buf.String()
				if !strings.HasPrefix(raw, "\r\x1b[2K") || strings.Contains(raw, "\n") || !utf8.ValidString(raw) {
					t.Fatalf("invalid frame: %q", raw)
				}
				line := styles.ReplaceAllString(strings.TrimPrefix(raw, "\r\x1b[2K"), "")
				cells := 0
				for _, r := range line {
					cells++
					if r == '界' || r == '😀' || r == '报' || r == '告' {
						cells++
					}
				}
				if cells >= columns {
					t.Fatalf("%d cells wrap a %d-column terminal: %q", cells, columns, raw)
				}
				if strings.Contains(line, "\x1b") || (!color && strings.Contains(raw, "\x1b[0m")) {
					t.Fatalf("unexpected styling: %q", raw)
				}
			}
		}
	}
}

func TestProgressLifecycle(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgress(&buf, "file.bin", 100, true)
	p.add(50)
	if !strings.Contains(buf.String(), blue) || !strings.Contains(buf.String(), "50%") {
		t.Fatalf("missing active color/count: %q", buf.String())
	}
	buf.Reset()
	p.last = time.Now().Add(time.Hour)
	p.add(10)
	if buf.Len() != 0 {
		t.Fatalf("redraw was not throttled: %q", buf.String())
	}
	p.start = time.Now().Add(-time.Hour)
	r := p.Reader(strings.NewReader("payload")).(io.ReadSeeker)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if p.done != 0 || time.Since(p.start) > time.Minute || !p.last.IsZero() {
		t.Fatal("retry did not reset the count, rate and redraw throttle")
	}
	p.add(100)
	p.Finish()
	final := buf.String()
	if !strings.Contains(final, green) || !strings.Contains(final, "100%") || strings.Count(final, "\n") != 1 {
		t.Fatalf("missing final state: %q", final)
	}
	p.Finish()
	p.Abort()
	p.add(1)
	if buf.String() != final {
		t.Fatal("stopped progress wrote more output")
	}
}

func TestProgressAbortAndCleanLabel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgress(&buf, "file\n\x1b[2J\u202e.bin", 100, false)
	p.add(50)
	frame := buf.String()
	if strings.ContainsAny(frame, "\n\u202e") || strings.Contains(frame, "\x1b[2J") {
		t.Fatalf("label contains terminal controls: %q", frame)
	}
	buf.Reset()
	p.Abort()
	p.Abort()
	p.Finish()
	p.add(50)
	if buf.String() != "\r\x1b[K" {
		t.Fatalf("abort output: %q", buf.String())
	}
}
