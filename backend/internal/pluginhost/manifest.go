// Package pluginhost installs, verifies and drives out-of-process plugins.
// A plugin provides content sources the core deliberately does not implement
// (docs/PLUGINS.md): the backend uses it to list a source's catalog, the
// client uses it to resolve a playable URL at playback time.
//
// This package knows the protocol, the artifact layout and the trust rules,
// and nothing about any particular site.
package pluginhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Protocol is the host protocol version this build speaks. A plugin declaring
// a higher number may use shapes this host does not understand, so it is
// refused instead of being driven half-wrong.
const Protocol = 1

// Capabilities a manifest may declare.
const (
	CapabilityCatalogList  = "catalog.list"
	CapabilityMediaResolve = "media.resolve"
)

// Manifest is a plugin's plugin.json, and also the result of the `info` call.
type Manifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Version      string   `json:"version"`
	Protocol     int64    `json:"protocol"`
	Entrypoint   string   `json:"entrypoint"`
	Capabilities []string `json:"capabilities"`
	// Config declares what a source of this plugin can be configured with, so a
	// settings screen can be built from the plugin's own description instead of
	// the host knowing its keys. Empty means "no declared settings".
	Config []ConfigField `json:"config,omitempty"`
}

// Field types a manifest may declare. They say how the value is written into
// the source's config, which is handed to the plugin verbatim: a `number` field
// is stored as a JSON number, a `bool` as a boolean, everything else as a
// string.
const (
	ConfigTypeChoice = "choice"
	ConfigTypeBool   = "bool"
	ConfigTypeNumber = "number"
	ConfigTypeText   = "text"
)

// ConfigField is one setting a source of this plugin accepts.
type ConfigField struct {
	// Key is the config member the value is written to.
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
	// Options are the values a `choice` accepts, in the order to offer them.
	Options []ConfigOption `json:"options,omitempty"`
	// Help is one line of plain text shown next to the setting.
	Help string `json:"help,omitempty"`
}

// ConfigOption is one value of a `choice`.
type ConfigOption struct {
	// Value is written into the source's config exactly as declared, so a
	// numeric setting offers numbers and a textual one offers strings:
	// `{"value": 1080, "label": "1080p"}`.
	Value json.RawMessage `json:"value"`
	Label string          `json:"label"`
}

// idPattern keeps ids usable as directory names and as URL path segments.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// versionPattern keeps a version usable as a directory name: no separators, no
// traversal, no surprises when the host builds an install path from it.
var versionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9.+-]{0,63}$`)

// ValidID reports whether id is a usable plugin id. Exported so the API can
// reject a media source that names a plugin the host could never install.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// ValidVersion reports whether v looks like a plugin version.
func ValidVersion(v string) bool { return versionPattern.MatchString(v) }

// ErrIncompatible is returned when a manifest cannot be driven by this host.
var ErrIncompatible = errors.New("pluginhost: incompatible plugin")

// ErrNotFound is returned when the registry has no such plugin (or version).
var ErrNotFound = errors.New("pluginhost: not found in the registry")

// ErrNoArtifact is returned when a version exists but ships no archive for this
// host's platform.
var ErrNoArtifact = errors.New("pluginhost: no artifact for this platform")

// configKeyPattern keeps a declared key usable as a config member name.
var configKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// validateConfig checks the declared settings. A field nobody can render is a
// broken release, so it is refused here rather than shown as an empty row.
func (m Manifest) validateConfig() error {
	seen := map[string]bool{}
	for _, f := range m.Config {
		if !configKeyPattern.MatchString(f.Key) {
			return fmt.Errorf("config field %q is not a usable key (want lowercase letters, digits and underscores)", f.Key)
		}
		if seen[f.Key] {
			return fmt.Errorf("config field %q is declared twice", f.Key)
		}
		seen[f.Key] = true
		if strings.TrimSpace(f.Label) == "" {
			return fmt.Errorf("config field %q has no label", f.Key)
		}
		switch f.Type {
		case ConfigTypeChoice:
			if len(f.Options) == 0 {
				return fmt.Errorf("config field %q is a choice with no options", f.Key)
			}
			for _, o := range f.Options {
				if len(bytes.TrimSpace(o.Value)) == 0 || !json.Valid(o.Value) {
					return fmt.Errorf("config field %q has an option with no valid value", f.Key)
				}
			}
		case ConfigTypeBool, ConfigTypeNumber, ConfigTypeText:
		default:
			return fmt.Errorf("config field %q has type %q, want one of choice, bool, number, text", f.Key, f.Type)
		}
	}
	return nil
}

// Validate checks a manifest before the host trusts anything else about the
// plugin. A malformed manifest is a broken release, not something to work
// around.
func (m Manifest) Validate() error {
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("%w: invalid id %q (want lowercase letters, digits and hyphens)", ErrIncompatible, m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: %s has no name", ErrIncompatible, m.ID)
	}
	if !versionPattern.MatchString(m.Version) {
		return fmt.Errorf("%w: invalid version %q", ErrIncompatible, m.Version)
	}
	if err := m.validateConfig(); err != nil {
		return fmt.Errorf("%w: %s", ErrIncompatible, err)
	}
	if m.Protocol != Protocol {
		return fmt.Errorf("%w: %s speaks protocol %d, this host speaks %d", ErrIncompatible, m.ID, m.Protocol, Protocol)
	}
	if err := validEntrypoint(m.Entrypoint); err != nil {
		return err
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("%w: %s declares no capabilities", ErrIncompatible, m.ID)
	}
	for _, c := range m.Capabilities {
		switch c {
		case CapabilityCatalogList, CapabilityMediaResolve:
		default:
			return fmt.Errorf("%w: %s declares unknown capability %q", ErrIncompatible, m.ID, c)
		}
	}
	return nil
}

// HasCapability reports whether the manifest declares c.
func (m Manifest) HasCapability(c string) bool {
	for _, have := range m.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// hasCapability reports whether the comma-separated capabilities stored on a
// domain.Plugin include c.
func hasCapability(stored, c string) bool {
	for _, have := range strings.Split(stored, ",") {
		if strings.TrimSpace(have) == c {
			return true
		}
	}
	return false
}

// validEntrypoint rejects anything that could escape the artifact directory or
// name a file that can never be executed.
func validEntrypoint(p string) error {
	if p == "" {
		return fmt.Errorf("%w: manifest has no entrypoint", ErrIncompatible)
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return fmt.Errorf("%w: entrypoint %q must be a relative path with forward slashes", ErrIncompatible, p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: entrypoint %q is not a clean relative path", ErrIncompatible, p)
		}
	}
	return nil
}

// Artifact is one platform's archive inside a registry version.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// Version is one released version of a plugin.
type Version struct {
	Version   string     `json:"version"`
	Protocol  int64      `json:"protocol"`
	Artifacts []Artifact `json:"artifacts"`
}

// IndexPlugin is one plugin in a registry index.
type IndexPlugin struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Versions    []Version `json:"versions"`
}

// Index is a registry document (docs/PLUGINS.md "Registry").
type Index struct {
	Schema  int           `json:"schema"`
	Plugins []IndexPlugin `json:"plugins"`
}

// ParseIndex decodes and validates a registry document. Everything a host will
// later trust — ids, versions, hashes, URLs — is checked here, so a bad entry
// never reaches an install attempt.
func ParseIndex(raw []byte) (Index, error) {
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return Index{}, fmt.Errorf("pluginhost: parse registry: %w", err)
	}
	if idx.Schema != 1 {
		return Index{}, fmt.Errorf("pluginhost: registry schema %d is not supported", idx.Schema)
	}
	for i, p := range idx.Plugins {
		if !idPattern.MatchString(p.ID) {
			return Index{}, fmt.Errorf("pluginhost: registry plugin %d has invalid id %q", i, p.ID)
		}
		if strings.TrimSpace(p.Name) == "" {
			return Index{}, fmt.Errorf("pluginhost: registry plugin %s has no name", p.ID)
		}
		for j, v := range p.Versions {
			if !versionPattern.MatchString(v.Version) {
				return Index{}, fmt.Errorf("pluginhost: %s version %d has invalid version %q", p.ID, j, v.Version)
			}
			if v.Protocol <= 0 {
				return Index{}, fmt.Errorf("pluginhost: %s %s declares no protocol", p.ID, v.Version)
			}
			for k, a := range v.Artifacts {
				if err := a.validate(); err != nil {
					return Index{}, fmt.Errorf("pluginhost: %s %s artifact %d: %w", p.ID, v.Version, k, err)
				}
			}
		}
	}
	return idx, nil
}

func (a Artifact) validate() error {
	switch a.OS {
	case "linux", "windows", "darwin", "android":
	default:
		return fmt.Errorf("unsupported os %q", a.OS)
	}
	switch a.Arch {
	case "amd64", "arm64", "arm":
	default:
		return fmt.Errorf("unsupported arch %q", a.Arch)
	}
	if !sha256Pattern.MatchString(a.SHA256) {
		return fmt.Errorf("sha256 must be 64 lowercase hex characters")
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("url %q is not an absolute http(s) URL", a.URL)
	}
	return nil
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Find returns the plugin with this id.
func (idx Index) Find(id string) (IndexPlugin, bool) {
	for _, p := range idx.Plugins {
		if p.ID == id {
			return p, true
		}
	}
	return IndexPlugin{}, false
}

// Latest returns the newest version this host can run: the greatest version
// string among those whose protocol matches. Versions are ordered by string
// comparison, which is why the manifest must use an orderable scheme.
func (p IndexPlugin) Latest() (Version, bool) {
	var best Version
	found := false
	for _, v := range p.Versions {
		if v.Protocol != Protocol {
			continue
		}
		if !found || compareVersions(v.Version, best.Version) > 0 {
			best, found = v, true
		}
	}
	return best, found
}

// VersionByNumber returns one exact version.
func (p IndexPlugin) VersionByNumber(number string) (Version, bool) {
	if number == "" {
		return p.Latest()
	}
	for _, v := range p.Versions {
		if v.Version == number {
			return v, true
		}
	}
	return Version{}, false
}

// ArtifactFor returns the archive for one platform.
func (v Version) ArtifactFor(os, arch string) (Artifact, bool) {
	for _, a := range v.Artifacts {
		if a.OS == os && a.Arch == arch {
			return a, true
		}
	}
	return Artifact{}, false
}

// compareVersions orders two version strings by their numeric and
// non-numeric runs (so "2026.9.1" < "2026.10.1", which a plain string compare
// gets wrong, and "1.4.0" < "1.10.0").
func compareVersions(a, b string) int {
	split := func(s string) []string {
		var out []string
		var cur strings.Builder
		digits := false
		for i, r := range s {
			isDigit := r >= '0' && r <= '9'
			if i > 0 && isDigit != digits {
				out = append(out, cur.String())
				cur.Reset()
			}
			digits = isDigit
			cur.WriteRune(r)
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
		}
		return out
	}
	as, bs := split(a), split(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.ParseInt(as[i], 10, 64)
		bn, berr := strconv.ParseInt(bs[i], 10, 64)
		if aerr == nil && berr == nil {
			switch {
			case an < bn:
				return -1
			case an > bn:
				return 1
			}
			continue
		}
		if c := strings.Compare(as[i], bs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
