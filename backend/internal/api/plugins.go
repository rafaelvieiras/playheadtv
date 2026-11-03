package api

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pluginhost"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// registerPlugins wires the plugin surface: what the registry offers, what this
// host has installed, installing/uninstalling/enabling, and the artifact bytes
// themselves. The core has no site-specific code here — a plugin is data plus
// a process (docs/PLUGINS.md).
func registerPlugins(mux Router, db storage.DB, host *pluginhost.Host) {
	mux.HandleFunc("GET /plugins", listPlugins(db, host))
	mux.HandleFunc("POST /plugins/{id}/install", installPlugin(host))
	mux.HandleFunc("PUT /plugins/{id}", updatePlugin(db, host))
	mux.HandleFunc("DELETE /plugins/{id}", uninstallPlugin(host))
	mux.HandleFunc("GET /plugins/{id}/{version}/{platform}", servePluginArtifact(host))
}

// pluginArtifactView is one artifact as a client sees it: the URL points at
// this backend, not at wherever the registry says, so a client only ever has to
// reach the backend it is already paired with.
type pluginArtifactView struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

type pluginVersionView struct {
	Version   string               `json:"version"`
	Protocol  int64                `json:"protocol"`
	Artifacts []pluginArtifactView `json:"artifacts"`
}

type pluginCatalogView struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Versions    []pluginVersionView `json:"versions"`
}

// installedPluginView is one installed plugin plus the settings its manifest
// declares, so a client can build a config screen without the core knowing a
// single one of the plugin's keys (docs/PLUGINS.md).
type installedPluginView struct {
	domain.Plugin
	ConfigFields []pluginhost.ConfigField `json:"config_fields"`
}

type pluginListResponse struct {
	// RegistryConfigured is false when the deployment has no registry URL:
	// nothing can be installed, and a client should say so rather than offer a
	// button that always fails.
	RegistryConfigured bool                  `json:"registry_configured"`
	Installed          []installedPluginView `json:"installed"`
	Plugins            []pluginCatalogView   `json:"plugins"`
}

func listPlugins(db storage.DB, host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installed, err := host.ListInstalled(r.Context())
		if err != nil {
			handleDBError(w, err)
			return
		}
		installedViews := make([]installedPluginView, 0, len(installed))
		for _, p := range installed {
			view := installedPluginView{Plugin: p}
			if manifest, ok := host.Manifest(r.Context(), p.ID); ok {
				view.ConfigFields = manifest.Config
			}
			if view.ConfigFields == nil {
				view.ConfigFields = []pluginhost.ConfigField{}
			}
			installedViews = append(installedViews, view)
		}
		idx, _, err := host.Index(r.Context())
		if err != nil {
			// An unreachable registry is not a broken backend: the installed
			// plugins still work, so report what is installed and no catalog.
			writeJSON(w, http.StatusOK, pluginListResponse{
				RegistryConfigured: host.RegistryConfigured(),
				Installed:          installedViews,
				Plugins:            []pluginCatalogView{},
			})
			return
		}
		views := make([]pluginCatalogView, 0, len(idx.Plugins))
		for _, p := range idx.Plugins {
			pv := pluginCatalogView{ID: p.ID, Name: p.Name, Description: p.Description}
			for _, v := range p.Versions {
				vv := pluginVersionView{Version: v.Version, Protocol: v.Protocol}
				for _, a := range v.Artifacts {
					vv.Artifacts = append(vv.Artifacts, pluginArtifactView{
						OS: a.OS, Arch: a.Arch, SHA256: a.SHA256, Size: a.Size,
						URL: "/plugins/" + p.ID + "/" + v.Version + "/" + a.OS + "-" + a.Arch,
					})
				}
				pv.Versions = append(pv.Versions, vv)
			}
			views = append(views, pv)
		}
		writeJSON(w, http.StatusOK, pluginListResponse{
			RegistryConfigured: host.RegistryConfigured(),
			Installed:          installedViews,
			Plugins:            views,
		})
	}
}

type installPluginRequest struct {
	Version string `json:"version"`
}

func installPlugin(host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req installPluginRequest
		if r.ContentLength > 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
		}
		if !host.RegistryConfigured() {
			writeError(w, http.StatusConflict, "this backend has no plugin registry configured (PLAYHEADTV_PLUGIN_REGISTRY_URL)")
			return
		}
		plugin, err := host.Install(r.Context(), r.PathValue("id"), req.Version)
		if err != nil {
			writePluginError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, plugin)
	}
}

type updatePluginRequest struct {
	Enabled bool `json:"enabled"`
}

func updatePlugin(db storage.DB, host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req updatePluginRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := host.SetEnabled(r.Context(), r.PathValue("id"), req.Enabled); err != nil {
			writePluginError(w, err)
			return
		}
		plugin, err := host.Installed(r.Context(), r.PathValue("id"))
		if err != nil {
			writePluginError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plugin)
	}
}

func uninstallPlugin(host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := host.Installed(r.Context(), r.PathValue("id")); err != nil {
			writePluginError(w, err)
			return
		}
		if err := host.Uninstall(r.Context(), r.PathValue("id")); err != nil {
			writePluginError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// servePluginArtifact streams one cached artifact: the backend is a mirror, so a
// client never needs to reach the registry's own host (docs/PLUGINS.md
// "Hosts serve it their own way"). The ETag is the artifact's SHA-256, which is
// also what the client verifies after downloading.
func servePluginArtifact(host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		osName, arch, ok := strings.Cut(r.PathValue("platform"), "-")
		if !ok || osName == "" || arch == "" {
			writeError(w, http.StatusNotFound, "platform must be <os>-<arch>")
			return
		}
		idx, _, err := host.Index(r.Context())
		if err != nil {
			writePluginError(w, err)
			return
		}
		entry, ok := idx.Find(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "unknown plugin")
			return
		}
		version, ok := entry.VersionByNumber(r.PathValue("version"))
		if !ok {
			writeError(w, http.StatusNotFound, "unknown plugin version")
			return
		}
		artifact, ok := version.ArtifactFor(osName, arch)
		if !ok {
			writeError(w, http.StatusNotFound, "no artifact for that platform")
			return
		}
		path, err := host.ArtifactPath(r.Context(), artifact)
		if err != nil {
			writePluginError(w, err)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			writeError(w, http.StatusBadGateway, "could not read the cached artifact")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			writeError(w, http.StatusBadGateway, "could not read the cached artifact")
			return
		}
		w.Header().Set("Content-Type", artifactContentType(path))
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, artifact.SHA256) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.Copy(w, f)
	}
}

func artifactContentType(path string) string {
	if strings.HasSuffix(path, ".zip") {
		return "application/zip"
	}
	return "application/gzip"
}

// writePluginError maps the host's typed failures onto status codes: an unknown
// plugin/version is 404, an artifact this host cannot run or a plugin it cannot
// drive is 400, anything else (registry or download trouble) is 502.
func writePluginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pluginhost.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, pluginhost.ErrNotInstalled):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, pluginhost.ErrIncompatible), errors.Is(err, pluginhost.ErrNoArtifact):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}
