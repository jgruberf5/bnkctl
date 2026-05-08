package ibm

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/resourcecontrollerv2"
)

// COSInstance is a thin view of an IBM Cloud Object Storage service
// instance — the fields bnkctl actually displays / uses.
type COSInstance struct {
	Name      string
	GUID      string // short ID (UUID-like)
	CRN       string // crn:v1:bluemix:public:cloud-object-storage:global:a/<account>:<guid>::
	State     string // active, removed, etc.
	AccountID string
	PlanID    string // service plan UUID; resolution to friendly name (standard/lite) deferred to v1.x
}

// ensureRC lazily constructs the Resource Controller client. COS instance
// CRUD all goes through Resource Controller (instances are generic IBM
// Cloud resources, even though their bucket/object I/O uses S3).
func (c *Client) ensureRC() (*resourcecontrollerv2.ResourceControllerV2, error) {
	if c.rc != nil {
		return c.rc, nil
	}
	auth := &core.IamAuthenticator{ApiKey: c.apiKey}
	rc, err := resourcecontrollerv2.NewResourceControllerV2(&resourcecontrollerv2.ResourceControllerV2Options{
		Authenticator: auth,
	})
	if err != nil {
		return nil, fmt.Errorf("constructing Resource Controller client: %w", err)
	}
	c.rc = rc
	return rc, nil
}

// ListCOSInstances enumerates every COS service instance the API key has
// access to in the current account. Handles pagination — IBM's default
// page size is 100 so most accounts list in one round trip, but we
// follow next_url just in case.
//
// Server-side filtering by service ID is possible but requires hardcoding
// the COS service catalog UUID; client-side CRN-substring filtering
// produces the same result without the version-pinning concern.
func (c *Client) ListCOSInstances(ctx context.Context) ([]COSInstance, error) {
	rc, err := c.ensureRC()
	if err != nil {
		return nil, err
	}

	var out []COSInstance
	var startToken *string

	for {
		opts := rc.NewListResourceInstancesOptions()
		if startToken != nil {
			opts.SetStart(*startToken)
		}

		res, _, err := rc.ListResourceInstancesWithContext(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("listing resource instances: %w", err)
		}
		if res == nil {
			break
		}

		for i := range res.Resources {
			r := &res.Resources[i]
			if r.CRN == nil || !strings.Contains(*r.CRN, ":cloud-object-storage:") {
				continue
			}
			out = append(out, toCOSInstance(r))
		}

		if res.NextURL == nil || *res.NextURL == "" {
			break
		}
		next := extractStartFromURL(*res.NextURL)
		if next == nil {
			break
		}
		startToken = next
	}
	return out, nil
}

// COSPlanUUIDs maps friendly plan names to IBM Cloud catalog UUIDs.
// Hardcoded — the IBM catalog is the source of truth and these values
// are stable across years, but if IBM rotates them, --plan-id lets the
// user override directly.
var COSPlanUUIDs = map[string]string{
	"standard": "744bfc56-d12c-4866-88d5-dac9139e0e5d",
	"lite":     "2fdf0c08-2d32-4f46-84b5-32e0c92fffd8",
}

// CreateCOSInstance provisions a new COS service instance under the
// given resource group.
//
// plan: friendly name ("standard", "lite") OR a plan UUID directly —
// useful when IBM ships a new tier we haven't mapped yet.
//
// target: usually "global" (COS instances are global; buckets carry
// region affinity). Pass a region only for Single-Site Location plans.
func (c *Client) CreateCOSInstance(ctx context.Context, name, resourceGroupID, plan, target string) (*COSInstance, error) {
	if name == "" {
		return nil, fmt.Errorf("instance name is empty")
	}
	if resourceGroupID == "" {
		return nil, fmt.Errorf("resource group ID is empty")
	}
	planID := plan
	if uuid, ok := COSPlanUUIDs[plan]; ok {
		planID = uuid
	}
	if !looksLikeUUID(planID) {
		return nil, fmt.Errorf("unknown plan %q (try standard or lite, or pass a plan UUID via --plan-id)", plan)
	}
	if target == "" {
		target = "global"
	}

	rc, err := c.ensureRC()
	if err != nil {
		return nil, err
	}
	opts := rc.NewCreateResourceInstanceOptions(name, target, resourceGroupID, planID)
	res, _, err := rc.CreateResourceInstanceWithContext(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("creating COS instance %q: %w", name, err)
	}
	if res == nil {
		return nil, fmt.Errorf("empty response creating COS instance %q", name)
	}
	inst := toCOSInstance(res)
	return &inst, nil
}

// DeleteCOSInstance removes a COS instance by ID or CRN. recursive=true
// deletes bound resources (HMAC keys, service-credentials, etc.) along
// with the instance — the safe default for "delete this instance and
// everything it owned".
func (c *Client) DeleteCOSInstance(ctx context.Context, idOrCRN string, recursive bool) error {
	if idOrCRN == "" {
		return fmt.Errorf("instance id/crn is empty")
	}
	rc, err := c.ensureRC()
	if err != nil {
		return err
	}
	opts := rc.NewDeleteResourceInstanceOptions(idOrCRN)
	if recursive {
		opts.SetRecursive(true)
	}
	if _, err := rc.DeleteResourceInstanceWithContext(ctx, opts); err != nil {
		return fmt.Errorf("deleting COS instance %s: %w", idOrCRN, err)
	}
	return nil
}

// looksLikeUUID is a cheap shape check (8-4-4-4-12 hex) — enough to
// reject obvious typos before the API rejects them with a noisier error.
func looksLikeUUID(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
}

// GetCOSInstanceByName returns the first COS instance with the given name.
// Returns a clear "not found" error when nothing matches — common when
// users typo the name.
func (c *Client) GetCOSInstanceByName(ctx context.Context, name string) (*COSInstance, error) {
	if name == "" {
		return nil, fmt.Errorf("instance name is empty")
	}
	all, err := c.ListCOSInstances(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("COS instance %q not found in current account", name)
}

func toCOSInstance(r *resourcecontrollerv2.ResourceInstance) COSInstance {
	out := COSInstance{}
	if r.Name != nil {
		out.Name = *r.Name
	}
	if r.GUID != nil {
		out.GUID = *r.GUID
	}
	if r.CRN != nil {
		out.CRN = *r.CRN
	}
	if r.State != nil {
		out.State = *r.State
	}
	if r.AccountID != nil {
		out.AccountID = *r.AccountID
	}
	if r.ResourcePlanID != nil {
		out.PlanID = *r.ResourcePlanID
	}
	return out
}

// extractStartFromURL pulls the "start" query parameter from a paginated
// next_url. Returns nil if absent or unparseable — caller stops paging.
func extractStartFromURL(u string) *string {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil
	}
	s := parsed.Query().Get("start")
	if s == "" {
		return nil
	}
	return &s
}
