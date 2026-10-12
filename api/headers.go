package api

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// APIVersion is the major version of the HTTP API served under APIPrefix.
// The CLI refuses to talk to a server that reports a different value.
const APIVersion = 1

// Route prefixes and collection paths of API v1.
const (
	APIPrefix   = "/api/v1"
	RouteDirs   = APIPrefix + "/dirs"
	RouteInfo   = APIPrefix + "/info"
	RouteObject = APIPrefix + "/objects"
	RouteCont   = APIPrefix + "/content"
	RouteMove   = APIPrefix + "/move"
	RouteTrash  = APIPrefix + "/trash"
	RouteLinks  = APIPrefix + "/links"
	RouteHealth = "/healthz"
)

// Request and response headers. Header names are case-insensitive on the wire;
// these are the canonical spellings.
const (
	HeaderRequestID   = "X-Request-ID"
	HeaderVisibility  = "X-Vela-Visibility"
	HeaderTitle       = "X-Vela-Title"
	HeaderDescription = "X-Vela-Description"
	HeaderTags        = "X-Vela-Tags"
	HeaderDisposition = "X-Vela-Disposition"
	HeaderExpiresAt   = "X-Vela-Expires-At"
	HeaderSHA256      = "X-Vela-SHA256"
)

// ExpiresNone is the X-Vela-Expires-At value that clears an expiry.
const ExpiresNone = "none"

// ObjectRoute returns the API path for metadata operations on an object or
// prefix (stat, PATCH, DELETE), with the path escaped per segment.
func ObjectRoute(p string) string { return RouteObject + "/" + EscapePath(p) }

// ContentRoute returns the API path for uploading or downloading content.
func ContentRoute(p string) string { return RouteCont + "/" + EscapePath(p) }

// FormatRevisionETag renders the ETag used for optimistic locking: "r<rev>".
func FormatRevisionETag(rev int64) string { return `"r` + strconv.FormatInt(rev, 10) + `"` }

// ParseRevisionETag parses an If-Match value produced by FormatRevisionETag.
// Weak validators (W/"r3") are rejected because If-Match requires strong
// comparison (RFC 9110, section 13.1.1).
func ParseRevisionETag(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if len(s) < 4 || s[0] != '"' || s[1] != 'r' || s[len(s)-1] != '"' {
		return 0, fmt.Errorf("invalid revision ETag %q (want \"r<number>\")", s)
	}
	rev, err := strconv.ParseInt(s[2:len(s)-1], 10, 64)
	if err != nil || rev < 1 {
		return 0, fmt.Errorf("invalid revision ETag %q", s)
	}
	return rev, nil
}

// EncodeHeaderText percent-encodes free text (title, description) so that any
// UTF-8 string survives transport in an HTTP header, which is limited to a
// subset of ASCII.
func EncodeHeaderText(s string) string { return url.PathEscape(s) }

// DecodeHeaderText reverses EncodeHeaderText.
func DecodeHeaderText(s string) (string, error) { return url.PathUnescape(s) }

// FormatTagsHeader joins tags for X-Vela-Tags.
func FormatTagsHeader(tags []string) string { return strings.Join(tags, ",") }

// ParseTagsHeader splits X-Vela-Tags. Empty items are ignored, so an empty
// header value means "no tags". Normalization happens in NormalizeTags.
func ParseTagsHeader(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
