package cos

import (
	"context"
	"fmt"

	"github.com/IBM/ibm-cos-sdk-go/aws"
	"github.com/IBM/ibm-cos-sdk-go/service/s3"
)

// CreateBucket creates a bucket bound to the client's COS instance. The
// region/class pair is encoded into a LocationConstraint string that IBM
// COS uses to pick the storage tier and locality.
func (c *Client) CreateBucket(ctx context.Context, name, class string) error {
	if name == "" {
		return fmt.Errorf("bucket name is empty")
	}
	_, err := c.s3.CreateBucketWithContext(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(name),
		CreateBucketConfiguration: &s3.CreateBucketConfiguration{
			LocationConstraint: aws.String(LocationConstraint(c.region, class)),
		},
	})
	if err != nil {
		return fmt.Errorf("creating bucket %s: %w", name, err)
	}
	return nil
}

// DeleteBucket removes a bucket. Fails if the bucket is non-empty —
// callers should empty the bucket first or expose --force in the CLI to
// do that for them (deferred to v1.x).
func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("bucket name is empty")
	}
	_, err := c.s3.DeleteBucketWithContext(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		return fmt.Errorf("deleting bucket %s: %w", name, err)
	}
	return nil
}

// ListBuckets returns every bucket in the client's COS instance.
// IBM COS scopes ListBuckets by the IAM credentials' instance ID, so
// only buckets in this Client's instance come back.
func (c *Client) ListBuckets(ctx context.Context) ([]string, error) {
	res, err := c.s3.ListBucketsWithContext(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("listing buckets: %w", err)
	}
	names := make([]string, 0, len(res.Buckets))
	for _, b := range res.Buckets {
		if b.Name != nil {
			names = append(names, *b.Name)
		}
	}
	return names, nil
}
