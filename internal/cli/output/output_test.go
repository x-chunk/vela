package output

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"go.xchunk.org/vela/api"
)

func TestDuration(t *testing.T) {
	t.Parallel()
	tests := map[time.Duration]string{
		-time.Second:                 "expired",
		0:                            "expired",
		45 * time.Second:             "45s",
		90 * time.Second:             "1m 30s",
		2*time.Hour + 5*time.Minute:  "2h 5m",
		3*24*time.Hour + 4*time.Hour: "3d 4h",
		29*24*time.Hour + 23*time.Hour + 59*time.Minute: "29d 23h",
	}
	for in, want := range tests {
		if got := Duration(in); got != want {
			t.Errorf("Duration(%v) = %q; want %q", in, got, want)
		}
	}
}

func TestPrinterModes(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	p := &Printer{Out: &out, Err: &errOut}
	p.Table([]string{"A", "LONG"}, [][]any{{"1", "x"}, {"22", "y"}})
	if out.String() != "A   LONG\n1   x\n22  y\n" {
		t.Fatalf("table = %q", out.String())
	}
	out.Reset()
	p.JSON = true
	p.Println("hidden")
	p.Table(nil, [][]any{{"x"}})
	if out.Len() != 0 {
		t.Fatalf("human output in JSON mode: %q", out.String())
	}
	if err := p.JSONValue(map[string]string{"a": "<b>"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\n  \"a\": \"<b>\"\n}\n" {
		t.Fatalf("json = %q", out.String())
	}
	p.Quiet = true
	p.Infof("info")
	p.Warnf("careful")
	if errOut.String() != "warning: careful\n" {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestColors(t *testing.T) {
	t.Parallel()
	plain := &Printer{}
	if plain.Visibility(api.Public) != "public  " || plain.Dir("d/") != "d/" {
		t.Fatal("colors without a terminal")
	}
	color := &Printer{Color: true}
	if !strings.Contains(string(color.Visibility(api.Private)), "\x1b[") {
		t.Fatal("no color on a terminal")
	}
}

func TestProgress(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgress(&buf, "file.bin", 1000, false)
	r := p.Reader(strings.NewReader(strings.Repeat("x", 1000)))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	p.Finish()
	p.Finish()
	if !strings.Contains(buf.String(), "100%") || !strings.HasSuffix(buf.String(), "\n") || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("progress output = %q", buf.String())
	}
	seek := p.Reader(strings.NewReader("abc")).(io.Seeker)
	if _, err := seek.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
}

// TestTerminalEscapesAreNeutralized covers R-009 at the printer level.
func TestTerminalEscapesAreNeutralized(t *testing.T) {
	t.Parallel()
	evil := "a\x1b]52;c;ZXZpbA==\x07b\x1b[2Jc\rd\be\u202eF\u009bG"
	want := `a\x1b]52;c;ZXZpbA==\x07b\x1b[2Jc\x0dd\x08e\u202eF\u009bG`
	if got := Clean(evil); got != want {
		t.Fatalf("Clean = %q\nwant   %q", got, want)
	}
	if got := CleanMultiline("line1\nline2\x1b"); got != "line1\nline2\\x1b" {
		t.Fatalf("CleanMultiline = %q", got)
	}
	if Clean("Контрольная работа 😀") != "Контрольная работа 😀" {
		t.Fatal("ordinary text changed")
	}
	for _, color := range []bool{false, true} {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &out, Color: color}
		p.Table([]string{"H"}, [][]any{{evil, p.Dir(evil)}})
		p.KeyValues([]KV{{Key: "Title", Value: evil}, {Key: "Desc", Value: "one\ntwo\x1b[5m"}})
		p.Println(evil)
		p.Printf("%s %d\n", evil, 7)
		p.Infof("%s", evil)
		p.Warnf("%v", evil)
		p.Errorf("x:", "%s", evil)
		s := out.String()
		for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\r", "\b", "\u202e", "\x1b[5m"} {
			if strings.Contains(s, bad) {
				t.Errorf("color=%v: output contains %q:\n%q", color, bad, s)
			}
		}
		if !strings.Contains(s, "one\n") || !strings.Contains(s, "two") || !strings.Contains(s, " 7\n") {
			t.Errorf("color=%v: legitimate content lost: %q", color, s)
		}
	}
}
