package api

import (
	"net/http"

	"github.com/rafaelvieiras/playheadtv/backend/internal/buildinfo"
)

// versionResponse is the body of GET /version. It is public (like /healthz):
// the client reads it before it has any credentials to decide whether it can
// talk to this backend at all, and an orchestrator or an operator can read the
// running release from it.
type versionResponse struct {
	// Version is the backend release ("dev" for a build without a version).
	Version string `json:"version"`
	// APIVersion is the integer version of the REST contract.
	APIVersion int `json:"api_version"`
	// Commit is the source revision, omitted when unknown.
	Commit string `json:"commit,omitempty"`
}

// VersionHandler serves GET /version. It is mounted by cmd/server next to
// /healthz rather than through Register: like the health check it is an
// operational endpoint (not a REST resource with an MCP counterpart), and
// RequireAuth lets it through unauthenticated (isOpenPath).
func VersionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, versionResponse{
			Version:    buildinfo.Version,
			APIVersion: buildinfo.APIVersion,
			Commit:     buildinfo.Commit,
		})
	}
}
