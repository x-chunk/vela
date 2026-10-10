package output

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
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
	width   int
	stopped bool
}

// NewProgress starts a bar for a transfer of total bytes.
func NewProgress(w io.Writer, label string, total int64) *Progress {
	return &Progress{w: w, label: label, total: total, start: time.Now(), width: 24}
}

// Reader wraps r so that reads advance the bar.
func (p *Progress) Reader(r io.Reader) io.Reader { return &progressReader{r: r, p: p} }

// Writer returns a writer that advances the bar.
func (p *Progress) Writer() io.Writer { return progressWriter{p: p} }

// Reset restarts the count, for example when an upload is retried.
func (p *Progress) Reset() {
	p.mu.Lock()
	p.done = 0
	p.mu.Unlock()
}

func (p *Progress) add(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	filled := int(frac * float64(p.width))
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", p.width-filled)
	rate := ""
	if el := now.Sub(p.start).Seconds(); el > 0.2 {
		rate = Size(int64(float64(p.done)/el)) + "/s"
	}
	label := p.label
	if len(label) > 32 {
		label = "..." + label[len(label)-29:]
	}
	_, _ = fmt.Fprintf(p.w, "\r%-32s [%s] %3.0f%% %9s / %-9s %10s\x1b[K", label, bar, frac*100, Size(p.done), Size(p.total), rate)
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
