package client_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.xchunk.org/vela/client"
)

type redirectTransport func(*http.Request) (*http.Response, error)

func (f redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedirectTransportErrorIsRedacted(t *testing.T) {
	const secret = "test-presigned-secret"
	cause := errors.New("custom transport failed at https://s3.example/?X-Amz-Signature=" + secret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://s3.example/blob?X-Amz-Signature="+secret, http.StatusFound)
	}))
	defer srv.Close()
	var trace bytes.Buffer
	hc := &http.Client{Transport: redirectTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Has("X-Amz-Signature") {
			return nil, cause
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	c, err := client.New(srv.URL, "test-api-key", client.WithHTTPClient(hc), client.WithTrace(&trace), client.WithRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Download(context.Background(), "private.txt")
	var ne *client.NetworkError
	var ue *url.Error
	if !errors.As(err, &ne) || !errors.As(err, &ue) || !errors.Is(err, cause) {
		t.Fatalf("lost transport cause: %v", err)
	}
	for _, output := range []string{err.Error(), ne.Err.Error(), trace.String()} {
		if strings.Contains(output, secret) || strings.Contains(output, "X-Amz-Signature") {
			t.Fatal("credential leaked in diagnostics")
		}
	}
}
