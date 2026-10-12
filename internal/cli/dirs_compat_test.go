package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.xchunk.org/vela/api"
)

// The legacy handler delegates unknown directory routes to the authenticated
// unknown-route fallback, matching a server from before directory policies.
func legacyDirectoryServer(t *testing.T, h *harness) *httptest.Server {
	t.Helper()
	handler := h.ts.Server.Handler()
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == api.RouteDirs || strings.HasPrefix(r.URL.Path, api.RouteDirs+"/") {
			unknown := r.Clone(r.Context())
			unknown.URL.Path = api.APIPrefix + "/legacy-unknown"
			unknown.URL.RawPath = ""
			handler.ServeHTTP(w, unknown)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(legacy.Close)
	h.env["VELA_URL"] = legacy.URL
	return legacy
}

func TestExistingCommandsAgainstLegacyDirectoryServer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seed("docs/file", "public", api.Public)
	legacyDirectoryServer(t, h)
	h.ok("ls")
	h.ok("ls", "-l", "--json")
	if r := h.ok("ls", "-l"); !strings.Contains(r.stdout, "docs/") {
		t.Fatal(r.stdout)
	}
	var url map[string]string
	h.ok("url", "docs/", "--json").json(t, &url)
	if url["url"] != h.ts.URL+"/docs/" {
		t.Fatal(url)
	}
	if r := h.ok("url", "/"); strings.TrimSpace(r.stdout) != h.ts.URL+"/" {
		t.Fatal(r.stdout)
	}
	var opened string
	h.run = func(_ context.Context, _ io.Reader, _ bool, _ string, args ...string) error {
		opened = args[len(args)-1]
		return nil
	}
	h.ok("open", "docs/")
	if opened != h.ts.URL+"/docs/" {
		t.Fatal(opened)
	}
	if r := h.exec("", "dir", "ls"); r.code != exitNotFound || !strings.Contains(r.stderr, "no such API endpoint") {
		t.Fatal(r)
	}
}

func TestDirectoryFallbackPreservesSupportedServerErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   api.ErrorCode
		exit   int
	}{
		{"unauthorized", 401, api.CodeUnauthorized, exitAuth},
		{"forbidden", 403, api.CodeForbidden, exitAuth},
		{"unavailable", 503, api.CodeUnavailable, exitNetwork},
		{"internal", 500, api.CodeInternal, exitError},
		{"invalid", 400, api.CodeInvalidRequest, exitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.seed("docs/file", "public", api.Public)
			handler := h.ts.Server.Handler()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == api.RouteDirs || strings.HasPrefix(r.URL.Path, api.RouteDirs+"/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(api.ErrorResponse{Error: &api.Error{Code: tc.code, Message: "directory policy request failed"}})
					return
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(proxy.Close)
			h.env["VELA_URL"] = proxy.URL
			h.onRequest = func(r *http.Request) {
				if r.URL.Path == api.RouteInfo {
					t.Error("policy failure incorrectly fell back to legacy URL")
				}
			}
			for _, args := range [][]string{{"ls", "-l"}, {"url", "docs/"}, {"open", "docs/"}} {
				r := h.exec("", args...)
				if r.code != tc.exit {
					t.Fatalf("%v: %+v", args, r)
				}
			}
		})
	}
	t.Run("individual 404 with collection supported", func(t *testing.T) {
		h := newHarness(t)
		h.seed("docs/file", "public", api.Public)
		handler := h.ts.Server.Handler()
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == api.DirRoute("docs/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(api.ErrorResponse{Error: &api.Error{Code: api.CodeNotFound, Message: "directory lookup failed"}})
				return
			}
			handler.ServeHTTP(w, r)
		}))
		t.Cleanup(proxy.Close)
		h.env["VELA_URL"] = proxy.URL
		h.onRequest = func(r *http.Request) {
			if r.URL.Path == api.RouteInfo {
				t.Error("supported server 404 used legacy URL")
			}
		}
		for _, args := range [][]string{{"url", "docs/"}, {"open", "docs/"}} {
			r := h.exec("", args...)
			if r.code != exitNotFound || !strings.Contains(r.stderr, "directory lookup failed") {
				t.Fatal(r)
			}
		}
	})
	t.Run("disabled index", func(t *testing.T) {
		h := newHarness(t)
		h.seed("docs/file", "public", api.Public)
		h.ok("dir", "set", "docs", "--index", "off")
		for _, args := range [][]string{{"url", "docs/"}, {"open", "docs/"}} {
			r := h.exec("", args...)
			if r.code != exitNotFound || !strings.Contains(r.stderr, "index is disabled") {
				t.Fatal(r)
			}
		}
	})
}
