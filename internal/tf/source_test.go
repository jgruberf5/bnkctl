package tf

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveLatestRelease_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/owner/repo/releases/latest"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		fmt.Fprint(w, `{"tag_name":"v1.2.3","draft":false,"prerelease":false}`)
	}))
	defer server.Close()

	prev := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = prev }()

	tag, err := ResolveLatestRelease(context.Background(), "owner/repo")
	if err != nil {
		t.Fatalf("ResolveLatestRelease: %v", err)
	}
	if tag != "v1.2.3" {
		t.Errorf("tag = %q, want v1.2.3", tag)
	}
}

func TestResolveLatestRelease_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	prev := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = prev }()

	_, err := ResolveLatestRelease(context.Background(), "owner/no-releases")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestResolveLatestRelease_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		http.Error(w, "rate limit", http.StatusForbidden)
	}))
	defer server.Close()

	prev := githubAPIBase
	githubAPIBase = server.URL
	defer func() { githubAPIBase = prev }()

	_, err := ResolveLatestRelease(context.Background(), "owner/repo")
	if err == nil {
		t.Fatal("expected error for rate-limit 403")
	}
}

func TestResolveLatestRelease_EmptyRepo(t *testing.T) {
	if _, err := ResolveLatestRelease(context.Background(), ""); err == nil {
		t.Error("expected error for empty repo")
	}
}
