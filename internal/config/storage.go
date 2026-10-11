package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.xchunk.org/vela/internal/units"
)

// R2Config contains explicit S3 credentials; no AWS provider chain is used.
type R2Config struct {
	AccountID         string
	Endpoint          string
	Bucket            string
	Prefix            string
	AccessKeyID       string
	SecretAccessKey   string
	Delivery          string
	PresignTTL        time.Duration
	PartSize          int64
	UploadConcurrency int
	PathStyle         bool
}

func (c *Config) loadStorage(lookup func(string) (string, bool)) error {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok {
			return v
		}
		return def
	}
	c.Storage = get("VELA_STORAGE", "fs")
	if c.Storage != "fs" && c.Storage != "r2" {
		return errors.New("VELA_STORAGE must be fs or r2")
	}
	var err error
	c.MaxConcurrentUploads, err = strconv.Atoi(get("VELA_MAX_CONCURRENT_UPLOADS", "4"))
	if err != nil || c.MaxConcurrentUploads < 1 || c.MaxConcurrentUploads > 1024 {
		return errors.New("VELA_MAX_CONCURRENT_UPLOADS must be between 1 and 1024")
	}
	r := &c.R2
	r.AccountID = get("VELA_R2_ACCOUNT_ID", "")
	r.Endpoint = get("VELA_R2_ENDPOINT", "")
	r.Bucket = get("VELA_R2_BUCKET", "")
	r.Prefix = get("VELA_R2_PREFIX", "blobs/")
	r.AccessKeyID = get("VELA_R2_ACCESS_KEY_ID", "")
	r.SecretAccessKey = get("VELA_R2_SECRET_ACCESS_KEY", "")
	r.Delivery = get("VELA_R2_DELIVERY", "proxy")
	r.PresignTTL, err = time.ParseDuration(get("VELA_R2_PRESIGN_TTL", "5m"))
	if err != nil {
		return errors.New("VELA_R2_PRESIGN_TTL must be a duration")
	}
	r.PartSize, err = units.ParseSize(get("VELA_R2_PART_SIZE", "8MiB"))
	if err != nil {
		return errors.New("VELA_R2_PART_SIZE must be a byte size")
	}
	r.UploadConcurrency, err = strconv.Atoi(get("VELA_R2_UPLOAD_CONCURRENCY", "2"))
	if err != nil {
		return errors.New("VELA_R2_UPLOAD_CONCURRENCY must be an integer")
	}
	r.PathStyle, err = strconv.ParseBool(get("VELA_R2_PATH_STYLE", "true"))
	if err != nil {
		return errors.New("VELA_R2_PATH_STYLE must be true or false")
	}
	if r.Endpoint == "" && r.AccountID != "" {
		r.Endpoint = "https://" + r.AccountID + ".r2.cloudflarestorage.com"
	}
	if c.Storage == "r2" {
		return r.Validate()
	}
	return nil
}

// Validate checks R2 settings even when it is only the migration destination.
func (r R2Config) Validate() error {
	for _, v := range []struct{ name, value string }{
		{"VELA_R2_ACCOUNT_ID", r.AccountID}, {"VELA_R2_BUCKET", r.Bucket}, {"VELA_R2_ACCESS_KEY_ID", r.AccessKeyID}, {"VELA_R2_SECRET_ACCESS_KEY", r.SecretAccessKey},
	} {
		if v.value == "" {
			return fmt.Errorf("%s is required for r2", v.name)
		}
	}
	u, err := url.Parse(r.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return errors.New("VELA_R2_ENDPOINT must be a bare http(s) origin without credentials or query")
	}
	if strings.ContainsAny(r.Bucket, "/\\\r\n") || strings.TrimSpace(r.Bucket) != r.Bucket {
		return errors.New("VELA_R2_BUCKET must be a bucket name")
	}
	if r.Prefix == "" || !strings.HasSuffix(r.Prefix, "/") || strings.HasPrefix(r.Prefix, "/") || strings.ContainsAny(r.Prefix, "\\\r\n") {
		return errors.New("VELA_R2_PREFIX must be a nonempty relative prefix ending in /")
	}
	for _, part := range strings.Split(strings.TrimSuffix(r.Prefix, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("VELA_R2_PREFIX has an invalid segment")
		}
	}
	if r.Delivery != "proxy" && r.Delivery != "redirect" {
		return errors.New("VELA_R2_DELIVERY must be proxy or redirect")
	}
	if r.PresignTTL < time.Second || r.PresignTTL > time.Hour {
		return errors.New("VELA_R2_PRESIGN_TTL must be between 1s and 1h")
	}
	if r.PartSize < 5<<20 || r.PartSize > 64<<20 {
		return errors.New("VELA_R2_PART_SIZE must be between 5MiB and 64MiB")
	}
	if r.UploadConcurrency < 1 || r.UploadConcurrency > 16 {
		return errors.New("VELA_R2_UPLOAD_CONCURRENCY must be between 1 and 16")
	}
	return nil
}
