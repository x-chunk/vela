package output

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Progress draws a single-line progress bar on a terminal (stderr). It
// redraws at most every 100ms so that fast transfers are not slowed down by
// terminal output.
type Progress struct {
	mu      sync.Mutex
	w       io.Writer
	label   string
	total   int64
	done    int64
	start   time.Time
	last    time.Time
	color   bool
	columns func() int
	stopped bool
}

// NewProgress starts a bar for a transfer of total bytes. The caller enables
// color according to the terminal and the user's color preferences.
func NewProgress(w io.Writer, label string, total int64, color bool) *Progress {
	return &Progress{
		w: w, label: Clean(label), total: total, start: time.Now(), color: color,
		columns: func() int {
			if f, ok := w.(interface{ Fd() uintptr }); ok {
				if columns, _, err := term.GetSize(int(f.Fd())); err == nil && columns > 0 { // #nosec G115 -- file descriptors fit in int
					return columns
				}
			}
			return 80
		},
	}
}

// Reader wraps r so that reads advance the bar.
func (p *Progress) Reader(r io.Reader) io.Reader { return &progressReader{r: r, p: p} }

// Writer returns a writer that advances the bar.
func (p *Progress) Writer() io.Writer { return progressWriter{p: p} }

// Reset restarts the count, for example when an upload is retried.
func (p *Progress) Reset() {
	p.mu.Lock()
	p.done = 0
	p.start = time.Now()
	p.last = time.Time{}
	p.mu.Unlock()
}

func (p *Progress) add(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.done += int64(n)
	if now := time.Now(); now.Sub(p.last) >= 100*time.Millisecond {
		p.last = now
		p.draw(now)
	}
}

func (p *Progress) draw(now time.Time) {
	if p.stopped {
		return
	}
	frac := 0.0
	if p.total > 0 {
		frac = min(float64(p.done)/float64(p.total), 1)
	}
	// Leave the last column unused: wrapping makes a carriage return update
	// only the last physical row, leaving earlier frames in scrollback.
	columns := max(0, p.columns()-1)
	status := fmt.Sprintf("%3.0f%%", frac*100)
	if columns < 10 {
		_, _ = fmt.Fprintf(p.w, "\r\x1b[2K%s", status[:min(columns, len(status))])
		return
	}
	if columns >= 50 {
		status += "  " + Size(p.done) + " / " + Size(p.total)
	}
	if el := now.Sub(p.start).Seconds(); columns >= 72 && el > 0.2 {
		status += "  " + Size(int64(float64(p.done)/el)) + "/s"
	}
	label, labelWidth := "", 0
	if columns >= 40 {
		label, labelWidth = progressLabel(p.label, min(24, columns/4))
		if label != "" {
			label += "  "
			labelWidth += 2
		}
	}
	width := min(28, columns-labelWidth-len(status)-3)
	filled := int(frac * float64(width))
	bar := strings.Repeat("━", filled)
	style := blue
	if frac == 1 {
		style = green
	}
	if p.color && bar != "" {
		bar = style + bar + reset
	}
	empty := strings.Repeat("─", width-filled)
	if p.color && empty != "" {
		empty = dim + empty + reset
	}
	_, _ = fmt.Fprintf(p.w, "\r\x1b[2K%s[%s%s] %s", label, bar, empty, status)
}

// progressLabel keeps the filename end without splitting UTF-8. Budget two
// cells for every non-ASCII rune so wide filenames cannot wrap the bar;
// combining characters merely leave a little extra room.
func progressLabel(s string, budget int) (string, int) {
	cells := 0
	for _, r := range s {
		cells++
		if r >= utf8.RuneSelf {
			cells++
		}
	}
	if cells <= budget {
		return s, cells
	}
	cells = 3 // ellipsis
	start := len(s)
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:start])
		w := 1
		if r >= utf8.RuneSelf {
			w = 2
		}
		if cells+w > budget {
			break
		}
		cells += w
		start -= n
	}
	return "..." + s[start:], cells
}

// Finish draws the final state and ends the line.
func (p *Progress) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.draw(time.Now())
	p.stopped = true
	_, _ = fmt.Fprint(p.w, "\n")
}

// Abort clears the line without a final redraw.
func (p *Progress) Abort() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stopped {
		p.stopped = true
		_, _ = fmt.Fprint(p.w, "\r\x1b[K")
	}
}

type progressReader struct {
	r io.Reader
	p *Progress
}

func (pr *progressReader) Read(b []byte) (int, error) {
	n, err := pr.r.Read(b)
	pr.p.add(n)
	return n, err
}

// Seek lets retrying uploads rewind; the bar starts over.
func (pr *progressReader) Seek(offset int64, whence int) (int64, error) {
	s, ok := pr.r.(io.Seeker)
	if !ok {
		return 0, fmt.Errorf("progress: underlying reader cannot seek")
	}
	pos, err := s.Seek(offset, whence)
	if err == nil && pos == 0 {
		pr.p.Reset()
	}
	return pos, err
}

type progressWriter struct{ p *Progress }

func (pw progressWriter) Write(b []byte) (int, error) {
	pw.p.add(len(b))
	return len(b), nil
}
