//go:build e2e

// Package e2e runs velad behind a real nginx with the reference
// configuration from examples/nginx (fs-accel.conf and its snippets),
// TLS and the HTTP redirect server included, and checks what reaches the
// client and what reaches the logs. Run with "make test-e2e"; it needs
// Docker and uses host networking, so it runs on Linux.
package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const nginxImage = "nginx:1.30-alpine"

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// writeCert writes a self-signed certificate for store.example.com in the
// layout certbot uses.
func writeCert(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "store.example.com"},
		DNSNames:     []string{"store.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"fullchain.pem": {Type: "CERTIFICATE", Bytes: der},
		"privkey.pem":   {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// nginx is a running nginx container with the reference configuration.
type nginx struct {
	t     *testing.T
	name  string
	https string // https://127.0.0.1:port
	http  string // http://127.0.0.1:port
}

// startNginx runs the reference configuration with only the listen
// addresses and the upstream address replaced; every replacement must hit,
// so the test fails if the reference file changes shape.
func startNginx(t *testing.T, upstream, blobsDir string) *nginx {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	root := repoRoot(t)
	ref, err := os.ReadFile(filepath.Join(root, "examples", "nginx", "fs-accel.conf"))
	if err != nil {
		t.Fatal(err)
	}
	httpPort, httpsPort := freePort(t), freePort(t)
	conf := string(ref)
	for _, r := range []struct{ old, new string }{
		{"    listen 80;\n", fmt.Sprintf("    listen 127.0.0.1:%d;\n", httpPort)},
		{"    listen [::]:80;\n", ""},
		{"    listen 443 ssl;\n", fmt.Sprintf("    listen 127.0.0.1:%d ssl;\n", httpsPort)},
		{"    listen [::]:443 ssl;\n", ""},
		{"    server 127.0.0.1:8080;\n", fmt.Sprintf("    server %s;\n", upstream)},
	} {
		if strings.Count(conf, r.old) != 1 {
			t.Fatalf("reference config: %q not found exactly once", r.old)
		}
		conf = strings.Replace(conf, r.old, r.new, 1)
	}
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "default.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	certs := filepath.Join(tmp, "certs")
	if err := os.Mkdir(certs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCert(t, certs)

	name := fmt.Sprintf("vela-e2e-%d", httpsPort)
	args := []string{"run", "-d", "--rm", "--name", name, "--network", "host",
		"-v", filepath.Join(tmp, "default.conf") + ":/etc/nginx/conf.d/default.conf:ro",
		"-v", filepath.Join(root, "examples", "nginx") + ":/etc/nginx/snippets:ro",
		"-v", certs + ":/etc/letsencrypt/live/store.example.com:ro",
		"-v", blobsDir + ":/srv/vela/blobs:ro",
		nginxImage}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	n := &nginx{t: t, name: name,
		https: fmt.Sprintf("https://127.0.0.1:%d", httpsPort),
		http:  fmt.Sprintf("http://127.0.0.1:%d", httpPort)}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("nginx logs:\n%s", n.logs())
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := n.try("GET", n.https+"/healthz"); err == nil && r.status == http.StatusOK {
			return n
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("nginx did not become ready")
	return nil
}

// logs returns every log nginx wrote: the site's access and error logs and
// the container output (global error log).
func (n *nginx) logs() string {
	var b strings.Builder
	for _, f := range []string{"/var/log/nginx/vela.access.log", "/var/log/nginx/vela.error.log"} {
		out, _ := exec.Command("docker", "exec", n.name, "cat", f).CombinedOutput()
		fmt.Fprintf(&b, "== %s\n%s\n", f, out)
	}
	out, _ := exec.Command("docker", "logs", n.name).CombinedOutput()
	fmt.Fprintf(&b, "== docker logs\n%s\n", out)
	return b.String()
}

type response struct {
	status int
	header http.Header
	body   string
}

var client = &http.Client{
	Transport:     &http.Transport{TLSClientConfig: &tls.Config{ServerName: "store.example.com", InsecureSkipVerify: true}}, // #nosec G402 -- self-signed test certificate
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (n *nginx) try(method, rawURL string, headers ...string) (response, error) {
	var body io.Reader
	if method == "PUT" || method == "POST" {
		body = strings.NewReader(headers[len(headers)-1])
		headers = headers[:len(headers)-1]
	}
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return response{}, err
	}
	req.Host = "store.example.com"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	return response{status: res.StatusCode, header: res.Header, body: string(b)}, err
}

func (n *nginx) get(method, rawURL string, headers ...string) response {
	n.t.Helper()
	r, err := n.try(method, rawURL, headers...)
	if err != nil {
		n.t.Fatal(err)
	}
	return r
}

// put uploads through the API; the body is the last argument, as for POST.
func (n *nginx) put(key, path, content string, headers ...string) response {
	n.t.Helper()
	return n.get("PUT", n.https+"/api/v1/content/"+path, append(append([]string{"Authorization", "Bearer " + key}, headers...), content)...)
}
