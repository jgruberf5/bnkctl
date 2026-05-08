package ibm

import (
	"context"
	"os"
	"testing"
	"time"
)

// Integration tests skip when no real IBM Cloud creds are present.
// Run locally with `IBMCLOUD_API_KEY=… go test ./internal/ibm/...`.
// CI runs without creds and only exercises the unit paths (New rejection).

const integrationTimeout = 30 * time.Second

func TestNew_RejectsEmptyAPIKey(t *testing.T) {
	if _, err := New("", "us-south"); err == nil {
		t.Error("expected error for empty API key")
	}
}

func TestVerify_Integration(t *testing.T) {
	apiKey := os.Getenv("IBMCLOUD_API_KEY")
	if apiKey == "" {
		t.Skip("IBMCLOUD_API_KEY not set; skipping integration test")
	}
	region := envOr("IBMCLOUD_REGION", "us-south")

	c, err := New(apiKey, region)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	id, err := c.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.IAMID == "" {
		t.Error("expected non-empty IAMID")
	}
	if id.AccountID == "" {
		t.Error("expected non-empty AccountID")
	}
	if c.Identity() == nil {
		t.Error("expected Verify to populate cached Identity")
	}
	t.Logf("verified: %s", id)
}

func TestResolveResourceGroup_Integration(t *testing.T) {
	apiKey := os.Getenv("IBMCLOUD_API_KEY")
	if apiKey == "" {
		t.Skip("IBMCLOUD_API_KEY not set; skipping integration test")
	}

	c, err := New(apiKey, envOr("IBMCLOUD_REGION", "us-south"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// 'default' resource group exists in every IBM Cloud account.
	rgID, err := c.ResolveResourceGroup(ctx, "default")
	if err != nil {
		t.Fatalf("ResolveResourceGroup(default): %v", err)
	}
	if rgID == "" {
		t.Error("expected non-empty resource group ID")
	}
	t.Logf("default resource group ID = %s", rgID)

	// A nonsense name should error cleanly.
	_, err = c.ResolveResourceGroup(ctx, "this-rg-should-not-exist-bnkctl-test")
	if err == nil {
		t.Error("expected ResolveResourceGroup to error on missing name")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
