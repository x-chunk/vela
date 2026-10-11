package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestRedirectNeverForwardsVelaKey(t *testing.T) {
	for _, destination := range []string{"http://vela.example:9000/blob?X-Amz-Signature=test", "https://bucket.vela.example/blob?X-Amz-Signature=test", "http://vela.example:8080/blob?X-Amz-Signature=test"} {
		t.Run(destination, func(t *testing.T) {
			callbackCalls, requests := 0, 0
			hc := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				callbackCalls++
				if req.Header.Get("Authorization") != "" {
					t.Error("key visible to redirect callback")
				}
				req.Header.Set("Authorization", "Bearer callback-key")
				return nil
			}, Transport: redirectTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if requests == 1 {
					if r.Header.Get("Authorization") != "Bearer vela-key" {
						t.Fatal("initial request lost key")
					}
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {destination}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
					t.Fatal("redirect forwarded credentials or referrer")
				}
				if requests == 2 {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"http://vela.example:8080/second?X-Amz-Signature=second"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
			})}
			c, err := client.New("http://vela.example:8080", "vela-key", client.WithHTTPClient(hc))
			if err != nil {
				t.Fatal(err)
			}
			body, _, err := c.Download(context.Background(), "private.txt")
			if err != nil {
				t.Fatal(err)
			}
			_ = body.Close()
			if callbackCalls != 2 || requests != 3 {
				t.Fatalf("callbacks=%d requests=%d", callbackCalls, requests)
			}
			probe, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, destination, nil)
			if err := hc.CheckRedirect(probe, nil); err != nil || probe.Header.Get("Authorization") != "Bearer callback-key" {
				t.Fatal("caller HTTP client was changed")
			}
		})
	}
}

func TestRedirectPreservesStopPolicy(t *testing.T) {
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: redirectTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 302, Status: "302 Found", Header: http.Header{"Location": {"https://storage.example/blob"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}),
	}
	c, err := client.New("https://vela.example", "key", client.WithHTTPClient(hc), client.WithRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Download(context.Background(), "file"); err == nil {
		t.Fatal("redirect should remain unfollowed")
	}
}
