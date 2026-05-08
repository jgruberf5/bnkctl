package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
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
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	bnkctlRepo        = "jgruberf5/bnkctl"
	selfUpdateTimeout = 5 * time.Minute
)

// runSelfUpdate implements `bnkctl self update`:
//
//  1. fetch the latest release tag from GitHub
//  2. find the asset matching this OS/arch (goreleaser naming)
//  3. download tarball + checksums.txt
//  4. verify SHA256
//  5. extract the bnkctl binary
//  6. atomic-rename onto $0
//
// Windows is gated behind a clear error pointing at scoop — the OS
// won't let us replace a running .exe in place, and the side-by-side
// dance isn't worth coding before there's a Windows user to test.
func runSelfUpdate(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), selfUpdateTimeout)
	defer cancel()

	fmt.Fprintln(os.Stderr, "→ Checking for updates")
	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  Current: %s\n", Version)
	fmt.Fprintf(os.Stderr, "  Latest:  %s\n", rel.TagName)

	if Version == rel.TagName {
		fmt.Fprintln(os.Stderr, "✓ Already at latest")
		return nil
	}
	if Version == "dev" {
		fmt.Fprintln(os.Stderr, "  (current is a dev build; will update to the released tag)")
	}

	if runtime.GOOS == "windows" {
		return errors.New("in-place update not supported on Windows; use `scoop update bnkctl` or download from GitHub Releases")
	}

	if !promptYesNo("Update?", true) {
		return errors.New("aborted")
	}

	aName := assetName(rel.TagName)
	asset, ok := findAsset(rel.Assets, aName)
	if !ok {
		return fmt.Errorf("no asset matching %q in release %s — release may not have artefacts for %s/%s yet",
			aName, rel.TagName, runtime.GOOS, runtime.GOARCH)
	}
	sums, ok := findAsset(rel.Assets, "checksums.txt")
	if !ok {
		return errors.New("no checksums.txt in release; refusing to update without checksum verification")
	}

	fmt.Fprintf(os.Stderr, "→ Downloading %s (%d bytes)\n", asset.Name, asset.Size)
	tarball, err := httpGetBytes(ctx, asset.URL)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "→ Verifying checksum")
	expected, err := checksumFor(ctx, sums.URL, asset.Name)
	if err != nil {
		return err
	}
	actual := sha256Hex(tarball)
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset.Name, expected, actual)
	}

	fmt.Fprintln(os.Stderr, "→ Extracting binary")
	bin, err := extractBnkctlBinary(tarball)
	if err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	fmt.Fprintf(os.Stderr, "→ Replacing %s\n", self)
	if err := replaceBinary(self, bin); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Updated to %s\n", rel.TagName)
	return nil
}

// ── github metadata ─────────────────────────────────────────────────

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

func fetchLatestRelease(ctx context.Context) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", bnkctlRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "bnkctl")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("no releases for %s yet", bnkctlRepo)
	default:
		return nil, fmt.Errorf("github returned %s", resp.Status)
	}
	var r ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// assetName matches goreleaser's default name_template:
// "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}{ext}".
// .Version is the tag without leading 'v'.
func assetName(tag string) string {
	ver := strings.TrimPrefix(tag, "v")
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("bnkctl_%s_%s_%s%s", ver, runtime.GOOS, runtime.GOARCH, ext)
}

func findAsset(assets []ghAsset, name string) (ghAsset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// ── network + crypto helpers ────────────────────────────────────────

func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "bnkctl")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// checksumFor parses a goreleaser-style checksums.txt and returns the
// hex SHA256 for filename. Format: "<sha256>  <name>".
func checksumFor(ctx context.Context, url, filename string) (string, error) {
	body, err := httpGetBytes(ctx, url)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) >= 2 && parts[1] == filename {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("checksum not found for %s in checksums.txt", filename)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ── extraction + atomic replace ─────────────────────────────────────

// extractBnkctlBinary pulls the bnkctl binary out of a goreleaser
// tarball (which contains bnkctl + LICENSE + README.md at the top level).
func extractBnkctlBinary(tarball []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar reader: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) != "bnkctl" {
			continue
		}
		return io.ReadAll(tr)
	}
	return nil, errors.New("bnkctl binary not found in tarball")
}

// replaceBinary writes newBinary to a temp file in the same dir as
// target, chmods it executable, then atomically renames onto target.
//
// Same-dir is critical: rename is atomic only on the same filesystem,
// and brew/scoop/manual-install layouts have target on the system
// partition while /tmp is sometimes on tmpfs.
func replaceBinary(target string, newBinary []byte) error {
	dir := filepath.Dir(target)
	base := filepath.Base(target)

	tmp, err := os.CreateTemp(dir, "."+base+".tmp.*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w (write permission?)", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(newBinary); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("renaming onto %s: %w (try with sudo, or use brew/scoop upgrade)", target, err)
	}
	return nil
}
