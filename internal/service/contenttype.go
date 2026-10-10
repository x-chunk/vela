package service

import (
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
)

// extensionTypes is consulted before mime.TypeByExtension. The standard
// library reads /etc/mime.types, which differs between hosts and is absent in
// the distroless image, so without this table the same upload would get
// different types in development and production.
var extensionTypes = map[string]string{
	".7z":       "application/x-7z-compressed",
	".aac":      "audio/aac",
	".avif":     "image/avif",
	".bmp":      "image/bmp",
	".bz2":      "application/x-bzip2",
	".c":        "text/x-c",
	".conf":     "text/plain",
	".css":      "text/css",
	".csv":      "text/csv",
	".deb":      "application/vnd.debian.binary-package",
	".doc":      "application/msword",
	".docx":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".epub":     "application/epub+zip",
	".flac":     "audio/flac",
	".gif":      "image/gif",
	".go":       "text/x-go",
	".gz":       "application/gzip",
	".h":        "text/x-c",
	".heic":     "image/heic",
	".htm":      "text/html",
	".html":     "text/html",
	".ico":      "image/x-icon",
	".ics":      "text/calendar",
	".ini":      "text/plain",
	".iso":      "application/x-iso9660-image",
	".jpeg":     "image/jpeg",
	".jpg":      "image/jpeg",
	".js":       "text/javascript",
	".json":     "application/json",
	".log":      "text/plain",
	".m4a":      "audio/mp4",
	".m4v":      "video/mp4",
	".markdown": "text/markdown",
	".md":       "text/markdown",
	".mjs":      "text/javascript",
	".mkv":      "video/x-matroska",
	".mov":      "video/quicktime",
	".mp3":      "audio/mpeg",
	".mp4":      "video/mp4",
	".ndjson":   "application/x-ndjson",
	".odp":      "application/vnd.oasis.opendocument.presentation",
	".ods":      "application/vnd.oasis.opendocument.spreadsheet",
	".odt":      "application/vnd.oasis.opendocument.text",
	".oga":      "audio/ogg",
	".ogg":      "audio/ogg",
	".ogv":      "video/ogg",
	".opus":     "audio/opus",
	".otf":      "font/otf",
	".pdf":      "application/pdf",
	".png":      "image/png",
	".ppt":      "application/vnd.ms-powerpoint",
	".pptx":     "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".py":       "text/x-python",
	".rar":      "application/vnd.rar",
	".rs":       "text/x-rust",
	".rtf":      "application/rtf",
	".sh":       "text/x-shellscript",
	".sql":      "application/sql",
	".svg":      "image/svg+xml",
	".tar":      "application/x-tar",
	".tex":      "text/x-tex",
	".tgz":      "application/gzip",
	".tif":      "image/tiff",
	".tiff":     "image/tiff",
	".toml":     "application/toml",
	".ts":       "text/plain",
	".tsv":      "text/tab-separated-values",
	".ttf":      "font/ttf",
	".txt":      "text/plain",
	".vcf":      "text/vcard",
	".wasm":     "application/wasm",
	".wav":      "audio/wav",
	".webm":     "video/webm",
	".webp":     "image/webp",
	".woff":     "font/woff",
	".woff2":    "font/woff2",
	".xls":      "application/vnd.ms-excel",
	".xlsx":     "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xml":      "application/xml",
	".xz":       "application/x-xz",
	".yaml":     "application/yaml",
	".yml":      "application/yaml",
	".zip":      "application/zip",
	".zst":      "application/zstd",
}

// textTypes are non-text/* media types that are text and get a charset.
var textTypes = map[string]bool{
	"application/json":     true,
	"application/x-ndjson": true,
	"application/xml":      true,
	"application/toml":     true,
	"application/yaml":     true,
	"application/sql":      true,
	"image/svg+xml":        true,
}

// detectContentType implements the fallback chain from spec section 10.2:
// extension table, then the system MIME table, then content sniffing of the
// first 512 bytes.
func detectContentType(name string, sniff []byte) string {
	ext := strings.ToLower(path.Ext(name))
	ct := extensionTypes[ext]
	if ct == "" && ext != "" {
		ct = mime.TypeByExtension(ext)
	}
	if ct == "" {
		ct = http.DetectContentType(sniff)
	}
	norm, err := normalizeContentType(ct)
	if err != nil {
		return "application/octet-stream"
	}
	return norm
}

// normalizeContentType validates a media type, lower-cases it and adds
// "charset=utf-8" to text types that lack a charset. Without the charset a
// browser may guess the encoding of a text file wrong.
func normalizeContentType(ct string) (string, error) {
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return "", fmt.Errorf("invalid content type %q: %w", ct, err)
	}
	if !strings.Contains(mt, "/") {
		return "", fmt.Errorf("invalid content type %q: missing subtype", ct)
	}
	if strings.HasPrefix(mt, "text/") || textTypes[mt] {
		if _, ok := params["charset"]; !ok {
			if params == nil {
				params = map[string]string{}
			}
			params["charset"] = "utf-8"
		}
	}
	out := mime.FormatMediaType(mt, params)
	if out == "" {
		return "", fmt.Errorf("invalid content type %q", ct)
	}
	return out, nil
}

// isGenericContentType reports whether a client-sent type carries no
// information, in which case the server detects the type itself.
func isGenericContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return ct == "" || (err == nil && mt == "application/octet-stream")
}
