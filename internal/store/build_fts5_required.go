//go:build !sqlite_fts5

package store

// This file fails to compile when the `sqlite_fts5` build tag is missing.
//
// The mattn/go-sqlite3 driver only enables FTS5 when built with the
// `sqlite_fts5` build tag. Without it the binary links fine but the
// `messages_fts` virtual table (used by /search and the MCP `search_messages`
// tool) fails at runtime with `no such module: fts5`. We surface that as a
// compile-time error instead.
//
// Build with:
//
//	go build -tags sqlite_fts5 ./...
//
// The Makefile sets this for every Go target.
var _ = sigbrowseRequiresSqliteFts5BuildTag
