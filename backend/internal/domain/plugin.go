package domain

// Plugin is one installed plugin (docs/PLUGINS.md): an out-of-process artifact
// that lists a content source and/or resolves a playable URL. The core never
// implements the site-specific half — it installs, verifies, runs and talks to
// the plugin over a documented JSON-RPC protocol.
//
// A row is created only after the artifact's SHA-256 matched the registry's,
// so version/artifact_sha256 describe bytes that are actually on disk. There is
// one row per plugin id: installing another version replaces this one.
type Plugin struct {
	ID string `json:"id"`
	// Version is the plugin's own release version, free-form but orderable
	// ("2026.10.26", "1.4.0"). A media source's MinVersion is compared against
	// it by strings.Compare, so a plugin that switches scheme starts breaking
	// that comparison — keep the scheme stable.
	Version  string `json:"version"`
	Protocol int64  `json:"protocol"`
	// OS and Arch are the Go names of the platform the artifact was installed
	// for (linux/amd64, linux/arm64, windows/amd64, ...), so the host never
	// translates between naming schemes.
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Entrypoint     string `json:"entrypoint"`
	// Capabilities is comma-separated, as the manifest declared it
	// (catalog.list, media.resolve). A host only drives the capabilities it
	// needs; the backend lists, a client resolves.
	Capabilities string `json:"capabilities"`
	Enabled      bool   `json:"enabled"`
	InstalledAt  int64  `json:"installed_at"`
	UpdatedAt    int64  `json:"updated_at"`
}
