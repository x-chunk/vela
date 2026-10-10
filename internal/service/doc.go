// Package service implements Vela's business rules: visibility, the
// file/directory invariant, key scopes and prefixes, uploads, moves, the
// trash, signed links and the janitor.
//
// The package knows nothing about HTTP or SQL. It depends on the small
// interfaces declared in deps.go, which internal/wire satisfies with the
// SQLite store and the filesystem blob store. Every operation that acts on
// behalf of an API key receives a Principal and enforces its scope and path
// prefix here, so the rules hold no matter which transport calls in.
package service
