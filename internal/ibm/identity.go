package ibm

import (
	"context"
	"fmt"
	"strings"
)

// Identity describes who an IBM Cloud API key authenticates as. Populated
// by Client.Verify.
type Identity struct {
	IAMID       string // unique IAM identifier (IBMid-... for users, iam-ServiceId-... for service IDs)
	AccountID   string // BSS account the key belongs to
	Name        string // friendly name of the API key
	Description string // optional description set on the key
	IsServiceID bool   // true when the API key belongs to a service ID rather than a user
}

// String renders an identity for log/UI consumption: "name (IAMID) in
// account ACCOUNTID". Service IDs are tagged so the user knows.
func (id *Identity) String() string {
	tag := ""
	if id.IsServiceID {
		tag = " [service-id]"
	}
	switch {
	case id.Name != "":
		return fmt.Sprintf("%s%s (%s) in account %s", id.Name, tag, id.IAMID, id.AccountID)
	default:
		return fmt.Sprintf("%s%s in account %s", id.IAMID, tag, id.AccountID)
	}
}

// Verify exchanges the API key for IAM identity details. Use this to
// confirm credentials work and to learn the account context — needed
// by ResolveResourceGroup and any operation that scopes by account.
//
// Caches the result on the Client; subsequent calls re-fetch (so a
// long-running command can refresh) but Identity() returns the most
// recent cached value without an API call.
func (c *Client) Verify(ctx context.Context) (*Identity, error) {
	opts := c.iam.NewGetAPIKeysDetailsOptions()
	opts.SetIamAPIKey(c.apiKey)

	res, _, err := c.iam.GetAPIKeysDetailsWithContext(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("verifying IBM Cloud API key: %w", friendlyAuthErr(err))
	}

	id := &Identity{}
	if res.IamID != nil {
		id.IAMID = *res.IamID
		id.IsServiceID = strings.HasPrefix(id.IAMID, "iam-ServiceId-")
	}
	if res.AccountID != nil {
		id.AccountID = *res.AccountID
	}
	if res.Name != nil {
		id.Name = *res.Name
	}
	if res.Description != nil {
		id.Description = *res.Description
	}

	c.identity = id
	return id, nil
}

// friendlyAuthErr rewrites the SDK's noisy 401 ("BXNIM0415E ...") into
// something a user can act on. Anything else falls through unchanged.
func friendlyAuthErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "BXNIM0415E") || strings.Contains(msg, "Provided API key could not be found") {
		return fmt.Errorf("API key is invalid or revoked — check IBMCLOUD_API_KEY (raw error: %s)", msg)
	}
	return err
}
