// Package buildinfo holds what identifies this build: the release version
// (injected at link time) and the API version (a constant in the source).
//
// The two are independent on purpose (docs/ARCHITECTURE.md "Releases and
// updates"): Version says which release is running, APIVersion says which
// contract the REST API speaks. The client declares the range of API versions
// it supports and compares it against APIVersion, never against Version.
package buildinfo

// Version is the backend release, set at link time:
//
//	go build -ldflags "-X github.com/rafaelvieiras/playheadtv/backend/internal/buildinfo.Version=1.2.3"
//
// A build without the flag (go run, go test, a local docker build) reports
// "dev".
var Version = "dev"

// Commit is the source revision, set at link time like Version; empty when
// unknown.
var Commit = ""

// APIVersion is the integer version of the REST contract the clients consume.
// Bump it only on a change an older client cannot cope with (a removed or
// reshaped field or endpoint); additive changes keep it. The client's
// supported range lives in client/lib/services/compat.dart.
const APIVersion = 1
