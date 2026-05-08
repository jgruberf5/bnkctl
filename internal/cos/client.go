package cos

import (
	"fmt"

	"github.com/IBM/ibm-cos-sdk-go/aws"
	"github.com/IBM/ibm-cos-sdk-go/aws/credentials/ibmiam"
	"github.com/IBM/ibm-cos-sdk-go/aws/session"
	"github.com/IBM/ibm-cos-sdk-go/service/s3"
)

// iamTokenURL is IBM Cloud's IAM token exchange endpoint. Public; not
// region-specific. The COS S3 SDK uses this to swap an API key for a
// short-lived bearer token on each call.
const iamTokenURL = "https://iam.cloud.ibm.com/identity/token"

// Client wraps an IBM Cloud Object Storage S3 client. Bound to a single
// region and a single COS instance — multiple instances mean multiple
// Clients. One Client per command invocation.
type Client struct {
	region      string
	instanceCRN string
	s3          *s3.S3
}

// New constructs a COS S3 client. apiKey + instanceCRN authenticate via
// IBM IAM; region picks the regional S3 endpoint. The client uses
// path-style addressing (S3ForcePathStyle) which IBM COS prefers.
//
// instanceCRN is required: COS S3 operations (including ListBuckets)
// scope by instance via the IAM credentials. A bare API key without a
// service instance ID would get "no buckets" with no way to discover them.
func New(apiKey, region, instanceCRN string) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("api key is empty")
	}
	if region == "" {
		return nil, fmt.Errorf("region is empty")
	}
	if instanceCRN == "" {
		return nil, fmt.Errorf("COS instance CRN is empty")
	}

	conf := aws.NewConfig().
		WithRegion(region).
		WithEndpoint(EndpointForRegion(region)).
		WithCredentials(ibmiam.NewStaticCredentials(aws.NewConfig(), iamTokenURL, apiKey, instanceCRN)).
		WithS3ForcePathStyle(true)

	sess, err := session.NewSession()
	if err != nil {
		return nil, fmt.Errorf("creating COS session: %w", err)
	}

	return &Client{
		region:      region,
		instanceCRN: instanceCRN,
		s3:          s3.New(sess, conf),
	}, nil
}

// EndpointForRegion returns the cross-region public S3 endpoint URL for
// IBM Cloud Object Storage in a given region.
//
// For private (VPC-only) endpoints, the format is
// https://s3.private.{region}.cloud-object-storage.appdomain.cloud.
// Public is the v1 default.
func EndpointForRegion(region string) string {
	return fmt.Sprintf("https://s3.%s.cloud-object-storage.appdomain.cloud", region)
}

// LocationConstraint composes region + storage class into the
// LocationConstraint string IBM COS expects on bucket create.
//
//	us-south + standard → "us-south-standard"
//	ca-tor + smart      → "ca-tor-smart"
func LocationConstraint(region, class string) string {
	if class == "" {
		class = "standard"
	}
	return fmt.Sprintf("%s-%s", region, class)
}
