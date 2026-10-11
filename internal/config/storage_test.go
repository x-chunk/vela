package config

import (
	"strings"
	"testing"
)

func TestStorageSettings(t *testing.T) {
	base := map[string]string{"VELA_STORAGE": "r2", "VELA_R2_ACCOUNT_ID": "account", "VELA_R2_BUCKET": "test", "VELA_R2_ACCESS_KEY_ID": "key", "VELA_R2_SECRET_ACCESS_KEY": "secret", "VELA_ACCEL_PREFIX": "", "VELA_PUBLIC_URL": "https://store.example.com"}
	for _, test := range []struct {
		key, value string
		valid      bool
	}{
		{"VELA_STORAGE", "r2", true}, {"VELA_STORAGE", "other", false}, {"VELA_R2_BUCKET", "", false}, {"VELA_R2_SECRET_ACCESS_KEY", "", false}, {"VELA_R2_ENDPOINT", "https://user:secret@example.com", false}, {"VELA_R2_PREFIX", "../blobs/", false}, {"VELA_R2_PREFIX", "nested/blobs/", true}, {"VELA_R2_DELIVERY", "other", false}, {"VELA_R2_PART_SIZE", "1MiB", false}, {"VELA_R2_UPLOAD_CONCURRENCY", "0", false}, {"VELA_R2_PRESIGN_TTL", "2h", false}, {"VELA_MAX_CONCURRENT_UPLOADS", "0", false},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				if key == test.key {
					return test.value, true
				}
				v, ok := base[key]
				return v, ok
			}
			cfg, err := Load(nil, lookup)
			if (err == nil) != test.valid {
				t.Fatalf("config: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "user:secret") {
				t.Fatal("secret leaked")
			}
			if err == nil {
				if err := cfg.RequireServe(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	cfg, err := Load(nil, func(key string) (string, bool) {
		if key == "VELA_ACCEL_PREFIX" {
			return "/_blobs/", true
		}
		v, ok := base[key]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireServe(); err == nil {
		t.Fatal("r2+accel accepted")
	}
}
