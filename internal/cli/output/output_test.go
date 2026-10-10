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
	p.Table([]string{"A", "LONG"}, [][]string{{"1", "x"}, {"22", "y"}})
	if out.String() != "A   LONG\n1   x\n22  y\n" {
		t.Fatalf("table = %q", out.String())
	}
	out.Reset()
	p.JSON = true
	p.Println("hidden")
	p.Table(nil, [][]string{{"x"}})
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
	if !strings.Contains(color.Visibility(api.Private), "\x1b[") {
		t.Fatal("no color on a terminal")
	}
}

func TestProgress(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := NewProgress(&buf, "file.bin", 1000)
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
