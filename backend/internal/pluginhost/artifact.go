package pluginhost

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bounds on what a plugin artifact may cost this host. They exist so a broken
// or hostile registry entry cannot fill the disk: the numbers are far above any
// real plugin (a bundled yt-dlp release is ~40 MB with a dozen files).
const (
	maxArtifactBytes  = 512 << 20
	maxExtractedBytes = 1 << 30
	maxArchiveEntries = 20000
)

// artifactTimeout bounds one download. A plugin artifact is tens of megabytes;
// two minutes covers a slow link without hanging an install forever.
const artifactTimeout = 2 * time.Minute

// downloadArtifact returns a local path holding exactly the artifact at
// expectedSHA256, downloading it into the cache first when needed. The hash is
// checked in both cases: a cache file that no longer matches is re-downloaded.
func (h *Host) downloadArtifact(ctx context.Context, a Artifact) (string, error) {
	cachePath := filepath.Join(h.cacheDir(), a.SHA256)
	if ok, err := fileMatches(cachePath, a.SHA256); err == nil && ok {
		return cachePath, nil
	}

	if err := os.MkdirAll(h.cacheDir(), 0o755); err != nil {
		return "", fmt.Errorf("pluginhost: create artifact cache: %w", err)
	}
	tmp, err := os.CreateTemp(h.cacheDir(), ".download-*")
	if err != nil {
		return "", fmt.Errorf("pluginhost: create temp artifact: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath)
	}()

	ctx, cancel := context.WithTimeout(ctx, artifactTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("pluginhost: download artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pluginhost: download artifact: %s", resp.Status)
	}

	hash := sha256.New()
	body := io.TeeReader(io.LimitReader(resp.Body, maxArtifactBytes+1), hash)
	size, err := io.Copy(tmp, body)
	if err != nil {
		return "", fmt.Errorf("pluginhost: download artifact: %w", err)
	}
	if size > maxArtifactBytes {
		return "", fmt.Errorf("pluginhost: artifact exceeds %d bytes", maxArtifactBytes)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != a.SHA256 {
		return "", fmt.Errorf("pluginhost: artifact sha256 is %s, registry says %s", got, a.SHA256)
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		return "", fmt.Errorf("pluginhost: store artifact: %w", err)
	}
	return cachePath, nil
}

// fileMatches reports whether path exists and hashes to want.
func fileMatches(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(hash.Sum(nil)) == want, nil
}

// extractArtifact unpacks the archive into dest, refusing anything that would
// land outside it (path traversal, absolute paths, symlinks) and enforcing the
// size and entry-count bounds above.
func extractArtifact(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 2)
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if n == 2 && head[0] == 'P' && head[1] == 'K' {
		return extractZip(f, dest)
	}
	return extractTarGz(f, dest)
}

func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("pluginhost: artifact is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var extracted int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("pluginhost: read archive: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("pluginhost: archive has more than %d entries", maxArchiveEntries)
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		case tar.TypeReg:
		default:
			// Symlinks, devices and the rest have no place in a plugin
			// artifact; ignoring them is safer than extracting one.
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := hdr.FileInfo().Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		written, err := io.Copy(out, io.LimitReader(tr, maxExtractedBytes-extracted+1))
		closeErr := out.Close()
		if err != nil {
			return fmt.Errorf("pluginhost: extract %s: %w", hdr.Name, err)
		}
		if closeErr != nil {
			return closeErr
		}
		extracted += written
		if extracted > maxExtractedBytes {
			return fmt.Errorf("pluginhost: artifact expands beyond %d bytes", maxExtractedBytes)
		}
	}
}

func extractZip(r io.ReaderAt, dest string) error {
	// zip.Reader needs the size; artifacts are bounded, so read it off the
	// file-backed ReaderAt the caller already opened.
	size, err := readerSize(r)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("pluginhost: read zip artifact: %w", err)
	}
	if len(zr.File) > maxArchiveEntries {
		return fmt.Errorf("pluginhost: archive has more than %d entries", maxArchiveEntries)
	}
	var extracted int64
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		written, copyErr := io.Copy(out, io.LimitReader(rc, maxExtractedBytes-extracted+1))
		rc.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("pluginhost: extract %s: %w", f.Name, copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
		extracted += written
		if extracted > maxExtractedBytes {
			return fmt.Errorf("pluginhost: artifact expands beyond %d bytes", maxExtractedBytes)
		}
	}
	return nil
}

// readerSize returns the length of a file-backed ReaderAt.
func readerSize(r io.ReaderAt) (int64, error) {
	type lenner interface{ Stat() (os.FileInfo, error) }
	f, ok := r.(lenner)
	if !ok {
		return 0, errors.New("pluginhost: zip artifact is not file-backed")
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// safeJoin joins an archive entry name onto dir, refusing absolute names and
// any ".." traversal.
func safeJoin(dir, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("pluginhost: archive entry %q escapes the plugin directory", name)
	}
	target := filepath.Join(dir, clean)
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("pluginhost: archive entry %q escapes the plugin directory", name)
	}
	return target, nil
}

// readManifest decodes and validates dir/plugin.json.
func readManifest(dir string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("pluginhost: artifact has no plugin.json: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("pluginhost: parse plugin.json: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
