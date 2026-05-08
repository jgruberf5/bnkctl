package tf

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jgruberf5/bnkctl/internal/config"
)

func TestFetchSource_Local_OK(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.TFSourceCfg{Type: "local", Path: tmp}
	got, err := FetchSource(context.Background(), cfg, "")
	if err != nil {
		t.Fatalf("FetchSource: %v", err)
	}
	if got != tmp {
		t.Errorf("got %q, want %q", got, tmp)
	}
}

func TestFetchSource_Local_NotADir(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "regular-file")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.TFSourceCfg{Type: "local", Path: file}
	if _, err := FetchSource(context.Background(), cfg, ""); err == nil {
		t.Error("expected error for non-directory path")
	}
}

func TestFetchSource_Local_Missing(t *testing.T) {
	cfg := config.TFSourceCfg{Type: "local", Path: "/this/path/should/not/exist/bnkctl-test"}
	if _, err := FetchSource(context.Background(), cfg, ""); err == nil {
		t.Error("expected error for missing path")
	}
}

func TestFetchSource_Local_EmptyPath(t *testing.T) {
	cfg := config.TFSourceCfg{Type: "local", Path: ""}
	if _, err := FetchSource(context.Background(), cfg, ""); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestFetchSource_GitHub_NeedsRepoAndRef(t *testing.T) {
	cfg := config.TFSourceCfg{Type: "github", Repo: "", Ref: ""}
	if _, err := FetchSource(context.Background(), cfg, t.TempDir()); err == nil {
		t.Error("expected error for empty github repo/ref")
	}
}

func TestFetchSource_UnknownType(t *testing.T) {
	cfg := config.TFSourceCfg{Type: "ftp"}
	if _, err := FetchSource(context.Background(), cfg, ""); err == nil {
		t.Error("expected error for unknown source type")
	}
}
