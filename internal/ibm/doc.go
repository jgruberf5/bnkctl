// Package ibm wraps the IBM Cloud Go SDKs for everything that isn't COS
// bucket/object I/O:
//
//   - IAM Identity (auth verification, identity)            — github.com/IBM/platform-services-go-sdk/iamidentityv1
//   - Resource Manager (resource group lookup)              — github.com/IBM/platform-services-go-sdk/resourcemanagerv2
//   - Resource Controller (COS instance CRUD, future)       — github.com/IBM/platform-services-go-sdk/resourcecontrollerv2
//   - IKS / ROKS cluster config + status (future)           — github.com/IBM/container-services-go-sdk/kubernetesserviceapiv1
//
// COS bucket and object operations live in package cos (different SDK,
// S3-compatible API).
//
// Typical use:
//
//	c, err := ibm.New(apiKey, "us-south")
//	if err != nil { return err }
//
//	id, err := c.Verify(ctx)
//	if err != nil { return err }      // bad key, network, etc.
//
//	rgID, err := c.ResolveResourceGroup(ctx, "default")
package ibm
