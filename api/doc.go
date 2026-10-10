// Package api is the public contract between velad and its clients.
//
// It holds everything both sides must agree on byte for byte: DTOs, error
// codes, header and route names, and the rules for normalizing and validating
// object paths. The server and the vela CLI import this package instead of
// re-implementing the rules, so a path the CLI accepts is exactly a path the
// server accepts.
//
// The package is importable by third-party tools (for example edu-publish), so
// it must not depend on anything under go.xchunk.org/vela/internal. Changes to
// exported names, JSON field names or semantics are API changes; see AGENTS.md
// for what counts as a breaking change in v1.
package api
