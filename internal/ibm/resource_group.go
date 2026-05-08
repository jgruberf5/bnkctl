package ibm

import (
	"context"
	"fmt"
)

// ResolveResourceGroup looks up a resource group by name and returns its
// ID. Auto-verifies credentials (and populates Identity) if Verify has
// not yet been called — `bnkctl init` calls Verify explicitly first; later
// commands can skip and let this auto-verify.
//
// Returns a clear "not found" error when the name doesn't match any
// resource group — common when users' workspace config has the wrong
// region or a typo in the group name.
func (c *Client) ResolveResourceGroup(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("resource group name is empty")
	}
	if c.identity == nil {
		if _, err := c.Verify(ctx); err != nil {
			return "", fmt.Errorf("verifying credentials before resource group lookup: %w", err)
		}
	}
	accountID := c.identity.AccountID
	if accountID == "" {
		return "", fmt.Errorf("could not determine account ID after Verify; cannot scope resource group lookup")
	}

	opts := c.rmg.NewListResourceGroupsOptions()
	opts.SetName(name)
	opts.SetAccountID(accountID)

	res, _, err := c.rmg.ListResourceGroupsWithContext(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("listing resource groups: %w", err)
	}
	if res == nil || len(res.Resources) == 0 {
		return "", fmt.Errorf("resource group %q not found in account %s", name, accountID)
	}
	if res.Resources[0].ID == nil {
		return "", fmt.Errorf("resource group %q has no ID", name)
	}
	return *res.Resources[0].ID, nil
}
