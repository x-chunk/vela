package sign

import "testing"

func TestSignVerify(t *testing.T) {
	t.Parallel()
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	const id = "01jabcdefghjkmnpqrstvwxyz0"
	const exp = int64(1760100000)
	sig := Sign(key, id, exp)
	if len(sig) != 22 {
		t.Fatalf("signature length = %d, want 22", len(sig))
	}
	if !Verify(key, id, exp, sig) {
		t.Fatal("valid signature rejected")
	}
	other, _ := NewKey()
	tests := []struct {
		name string
		key  []byte
		id   string
		exp  int64
		sig  string
	}{
		{"extended exp", key, id, exp + 3600, sig},
		{"other link", key, "01jabcdefghjkmnpqrstvwxyz1", exp, sig}, // gitleaks:allow -- deterministic link ID, not a credential.
		{"rotated key", other, id, exp, sig},
		{"empty key", nil, id, exp, sig},
		{"truncated sig", key, id, exp, sig[:21]},
		{"garbage sig", key, id, exp, "!!!!"},
		{"empty sig", key, id, exp, ""},
	}
	for _, tt := range tests {
		if Verify(tt.key, tt.id, tt.exp, tt.sig) {
			t.Errorf("%s: forged signature accepted", tt.name)
		}
	}
}
