package ibm

import (
	"fmt"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/iamidentityv1"
	"github.com/IBM/platform-services-go-sdk/resourcecontrollerv2"
	"github.com/IBM/platform-services-go-sdk/resourcemanagerv2"
)

// Client wraps the IBM Cloud platform-services SDKs that bnkctl uses.
// Holds initialised service handles for IAM Identity (auth verification)
// and Resource Manager (resource group lookup). IAM and Resource Manager
// are global services — region only matters for region-bound operations
// (cluster config, COS) which construct their own service handles lazily.
//
// One Client per command invocation. Not safe for concurrent use across
// goroutines.
type Client struct {
	apiKey string
	region string

	iam *iamidentityv1.IamIdentityV1
	rmg *resourcemanagerv2.ResourceManagerV2
	rc  *resourcecontrollerv2.ResourceControllerV2 // lazily constructed by ensureRC

	// identity is populated by Verify and cached for subsequent calls
	// that need the AccountID (ResolveResourceGroup, COS instance ops, etc.).
	identity *Identity
}

// APIKey returns the API key the client was constructed with. Used by
// callers that need to forward the key into a downstream SDK (e.g., the
// COS S3 client which authenticates via IAM with the same key).
func (c *Client) APIKey() string { return c.apiKey }

// New constructs a Client. Does NOT validate the credentials — call
// Verify() to confirm the API key works before doing anything that
// would partially mutate state on a bad key.
func New(apiKey, region string) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("ibm cloud API key is empty")
	}
	auth := &core.IamAuthenticator{ApiKey: apiKey}

	iam, err := iamidentityv1.NewIamIdentityV1(&iamidentityv1.IamIdentityV1Options{
		Authenticator: auth,
	})
	if err != nil {
		return nil, fmt.Errorf("constructing IAM Identity client: %w", err)
	}

	rmg, err := resourcemanagerv2.NewResourceManagerV2(&resourcemanagerv2.ResourceManagerV2Options{
		Authenticator: auth,
	})
	if err != nil {
		return nil, fmt.Errorf("constructing Resource Manager client: %w", err)
	}

	return &Client{
		apiKey: apiKey,
		region: region,
		iam:    iam,
		rmg:    rmg,
	}, nil
}

// Region returns the region the client was constructed with.
func (c *Client) Region() string { return c.region }

// Identity returns the cached identity from the most recent Verify call,
// or nil if Verify hasn't been called yet.
func (c *Client) Identity() *Identity { return c.identity }
