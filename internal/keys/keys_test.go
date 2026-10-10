package keys

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateParse(t *testing.T) {
	t.Parallel()
	id, err := NewID()
	if err != nil || !ValidID(id) {
		t.Fatalf("NewID = %q, %v", id, err)
	}
	g, err := Generate(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g.Full, "vela_"+id+"_") || len(g.Full) != len("vela_")+10+1+43 {
		t.Fatalf("unexpected key format %q", g.Full)
	}
	gotID, h, err := Parse(g.Full)
	if err != nil || gotID != id || !bytes.Equal(h, g.SecretHash) {
		t.Fatalf("Parse = %q, %x, %v", gotID, h, err)
	}
	if !Verify(h, g.SecretHash) {
		t.Fatal("Verify rejected the right secret")
	}
	other, _ := Generate(id)
	_, h2, _ := Parse(other.Full)
	if Verify(h2, g.SecretHash) {
		t.Fatal("Verify accepted a different secret")
	}
	if Verify(h, nil) {
		t.Fatal("Verify accepted an empty stored hash")
	}
}

func TestParseMalformed(t *testing.T) {
	t.Parallel()
	g, _ := Generate("abcdefghjk")
	for _, bad := range []string{
		"",
		"vela_",
		"vela_abcdefghjk",
		"vela_abcdefghjk_",
		"vela_abcdefghj_" + g.Full[16:],  // short id
		"vela_ABCDEFGHJK_" + g.Full[16:], // upper-case id
		"vela_abcdefghjk-" + g.Full[16:], // wrong separator
		"key_abcdefghjk_" + g.Full[16:],
		g.Full + "A",
		g.Full[:len(g.Full)-1],
		"vela_abcdefghjk_!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!",
	} {
		if _, _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
	if _, err := Generate("bad"); err == nil {
		t.Error("Generate accepted a bad id")
	}
}

func TestMask(t *testing.T) {
	t.Parallel()
	g, _ := Generate("k3f9abcd12")
	if got := Mask(g.Full); got != "vela_k3f9abcd12_****" {
		t.Fatalf("Mask = %q", got)
	}
	if got := Mask("garbage"); got != "****" {
		t.Fatalf("Mask(garbage) = %q", got)
	}
}
