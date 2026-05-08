package tf

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jgruberf5/bnkctl/internal/config"
)

// FetchSource resolves a TFSourceCfg into a local directory containing
// the .tf files terraform will operate on.
//
// type=local — uses Path directly (verified to exist + be a dir).
// type=github — downloads the release tarball into baseDir/<repo-leaf>-<ref>/
// and returns that path. Idempotent: if the dir already exists with
// content, just returns it without re-downloading.
//
// stripping: GitHub tarballs have a single top-level dir (e.g.
// "ibmcloud_terraform_bigip_next_for_kubernetes_2_3-0.6.7/"); we strip
// it so the .tf files land directly under the dest.
func FetchSource(ctx context.Context, src config.TFSourceCfg, baseDir string) (string, error) {
	switch src.Type {
	case "local":
		if src.Path == "" {
			return "", fmt.Errorf("local TF source has empty path")
		}
		info, err := os.Stat(src.Path)
		if err != nil {
			return "", fmt.Errorf("local TF source %s: %w", src.Path, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("local TF source %s is not a directory", src.Path)
		}
		return src.Path, nil

	case "github":
		if src.Repo == "" || src.Ref == "" {
			return "", fmt.Errorf("github TF source needs both repo and ref (got repo=%q ref=%q)", src.Repo, src.Ref)
		}
		if baseDir == "" {
			return "", fmt.Errorf("baseDir is empty (where should the source be downloaded?)")
		}
		return downloadGitHubTarball(ctx, src.Repo, src.Ref, baseDir)

	default:
		return "", fmt.Errorf("unknown TF source type %q (want github or local)", src.Type)
	}
}

func downloadGitHubTarball(ctx context.Context, repo, ref, baseDir string) (string, error) {
	leaf := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		leaf = repo[i+1:]
	}
	dest := filepath.Join(baseDir, leaf+"-"+ref)

	// Already present? Reuse — release tags are immutable so re-download
	// would just give us the same bytes.
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return dest, nil
	}

	url := fmt.Sprintf("https://github.com/%s/archive/refs/tags/%s.tar.gz", repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "bnkctl")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", url, resp.Status)
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}

	// stripComponents=1: GitHub wraps everything in <repo>-<ref>/.
	if err := extractTarGz(resp.Body, dest, 1); err != nil {
		_ = os.RemoveAll(dest)
		return "", fmt.Errorf("extracting %s: %w", url, err)
	}
	return dest, nil
}

// extractTarGz extracts a gzip'd tarball into dest, stripping the first
// stripComponents leading path components from each entry — equivalent to
// `tar --strip-components=N`.
//
// Defenses: rejects entries that escape dest via "../"; skips symlinks
// (we don't want a tarball pointing at /etc/passwd); ignores anything
// other than regular files and directories.
func extractTarGz(r io.Reader, dest string, stripComponents int) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	cleanDest := filepath.Clean(dest)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		parts := strings.Split(filepath.ToSlash(hdr.Name), "/")
		if len(parts) <= stripComponents {
			continue
		}
		rel := filepath.Join(parts[stripComponents:]...)
		if rel == "" || rel == "." {
			continue
		}

		target := filepath.Join(cleanDest, rel)
		// Guard against ../ traversal.
		if !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) && target != cleanDest {
			return fmt.Errorf("tar entry escapes destination: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Symlinks, devices, etc. — skip silently.
		}
	}
}
