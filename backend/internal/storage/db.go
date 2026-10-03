// Package storage abstracts the SQLite-compatible store behind a single
// interface so the rest of the backend never branches on deployment tier.
//
// Two implementations share the same schema (migrations/0001_init.sql):
//   - sqlite.go (not yet implemented): a local SQLite file, the self-hosted
//     default — zero external dependency, matches "runs on the user's own
//     infra" exactly.
//   - d1.go (not yet implemented): Cloudflare D1 accessed over its HTTP
//     query API (POST /accounts/{account}/d1/database/{db}/query). This is
//     an external call from a normal long-running Go process — the backend
//     does NOT run inside a Cloudflare Worker/V8 isolate, Go has no native
//     Workers runtime. D1 is just a managed, globally-replicated SQLite
//     reached over HTTPS, used only by the cloud-sync tier.
//
// Edge caching for GET /epg (the read-heavy, cache-friendly endpoint) is a
// deployment concern, not a storage one: cloud-sync puts the Go server
// behind Cloudflare's normal reverse-proxy CDN (orange-cloud DNS) and relies
// on standard Cache-Control headers, purging the cached response when
// EpgRevision changes. Self-hosted deployments may or may not sit behind any
// CDN at all — the API contract (GET /epg + GET /epg/meta) doesn't assume
// one either way.
package storage

import "context"

// DB is intentionally minimal — enough for the domain layer to read/write
// rows without knowing which backend it's talking to.
type DB interface {
	Exec(ctx context.Context, query string, args ...any) error
	Query(ctx context.Context, dest any, query string, args ...any) error
	// Transact runs fn inside one transaction: everything fn does through tx
	// commits together when fn returns nil and is rolled back otherwise. Use
	// it to batch many small writes (one commit instead of one per
	// statement). fn must use tx, never the outer DB: the pool holds a
	// single connection, so touching the outer DB from inside fn deadlocks.
	Transact(ctx context.Context, fn func(tx DB) error) error
}
