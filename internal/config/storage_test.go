package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestStorageSettings(t *testing.T) {
	base := map[string]string{"VELA_STORAGE": "r2", "VELA_R2_ACCOUNT_ID": "account", "VELA_R2_BUCKET": "test", "VELA_R2_ACCESS_KEY_ID": "key", "VELA_R2_SECRET_ACCESS_KEY": "secret", "VELA_ACCEL_PREFIX": "", "VELA_PUBLIC_URL": "https://store.example.com"}
	for _, test := range []struct {
		key, value string
		valid      bool
	}{
		{"VELA_STORAGE", "r2", true},
		{"VELA_R2_REGION", "us-east-1", true}, {"VELA_R2_REGION", "", false}, {"VELA_R2_REGION", "invalid/region", false},
		{"VELA_MAX_UPLOAD", "100GiB", false}, {"VELA_MAX_CREATE", "100GiB", false},
		{"VELA_MAX_UPLOAD", fmt.Sprint(int64(8<<20) * R2MaxUploadParts), true},
		{"VELA_MAX_UPLOAD", fmt.Sprint(int64(8<<20)*R2MaxUploadParts + 1), false}, {"VELA_STORAGE", "other", false}, {"VELA_R2_BUCKET", "", false}, {"VELA_R2_SECRET_ACCESS_KEY", "", false}, {"VELA_R2_ENDPOINT", "https://user:secret@example.com", false}, {"VELA_R2_PREFIX", "../blobs/", false}, {"VELA_R2_PREFIX", "nested/blobs/", true}, {"VELA_R2_DELIVERY", "other", false}, {"VELA_R2_PART_SIZE", "1MiB", false}, {"VELA_R2_UPLOAD_CONCURRENCY", "0", false}, {"VELA_R2_PRESIGN_TTL", "2h", false}, {"VELA_MAX_CONCURRENT_UPLOADS", "0", false},
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

func TestS3EndpointWithoutCloudflareAccount(t *testing.T) {
	t.Parallel()
	vars := map[string]string{
		"VELA_STORAGE": "r2", "VELA_R2_ENDPOINT": "https://s3.example.com",
		"VELA_R2_REGION": "us-east-1", "VELA_R2_BUCKET": "test",
		"VELA_R2_ACCESS_KEY_ID": "example", "VELA_R2_SECRET_ACCESS_KEY": "example-secret",
		"VELA_PUBLIC_URL": "https://store.example.com",
	}
	cfg, err := Load(nil, env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireServe(); err != nil {
		t.Fatal(err)
	}
	if cfg.R2.AccountID != "" || cfg.R2.Region != "us-east-1" {
		t.Fatalf("unexpected S3 configuration: %+v", cfg.R2)
	}
	delete(vars, "VELA_R2_ENDPOINT")
	if _, err := Load(nil, env(vars)); err == nil || !strings.Contains(err.Error(), "VELA_R2_ACCOUNT_ID or VELA_R2_ENDPOINT") {
		t.Fatalf("missing endpoint accepted: %v", err)
	}
}

func TestR2UploadCapacity(t *testing.T) {
	for _, partSize := range []int64{5 << 20, 8 << 20, 16 << 20, 64 << 20} {
		r := R2Config{PartSize: partSize}
		for _, size := range []int64{0, partSize * R2MaxUploadParts, partSize*R2MaxUploadParts + 1} {
			err := r.ValidateUploadSize(size)
			if (err == nil) != (size <= partSize*R2MaxUploadParts) {
				t.Fatalf("part=%d size=%d error=%v", partSize, size, err)
			}
		}
	}
	for _, storage := range []string{"fs", "r2"} {
		cfg, err := Load(nil, func(key string) (string, bool) {
			settings := map[string]string{"VELA_STORAGE": storage, "VELA_MAX_UPLOAD": "100GiB", "VELA_R2_PART_SIZE": "16MiB", "VELA_R2_ACCOUNT_ID": "test", "VELA_R2_BUCKET": "test", "VELA_R2_ACCESS_KEY_ID": "key", "VELA_R2_SECRET_ACCESS_KEY": "secret"}
			value, ok := settings[key]
			return value, ok
		})
		if err != nil || cfg.MaxUpload != 100<<30 {
			t.Fatalf("storage=%s config=%+v error=%v", storage, cfg, err)
		}
	}
}
