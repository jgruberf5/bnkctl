package cos

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/IBM/ibm-cos-sdk-go/aws"
	"github.com/IBM/ibm-cos-sdk-go/service/s3"
	"github.com/IBM/ibm-cos-sdk-go/service/s3/s3manager"
)

// ObjectInfo describes an object as returned by ListObjects.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// PutObjectFromFile uploads the contents of localPath to bucket/key. Uses
// the s3manager Uploader so large files transparently use multipart with
// concurrent parts; small files take a single PUT.
func (c *Client) PutObjectFromFile(ctx context.Context, bucket, key, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", localPath, err)
	}
	defer f.Close()

	uploader := s3manager.NewUploaderWithClient(c.s3)
	_, err = uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return fmt.Errorf("uploading %s to %s/%s: %w", localPath, bucket, key, err)
	}
	return nil
}

// GetObjectToFile downloads bucket/key to localPath, creating or
// truncating the file. Uses the s3manager Downloader for chunked,
// concurrent gets on large objects.
func (c *Client) GetObjectToFile(ctx context.Context, bucket, key, localPath string) error {
	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", localPath, err)
	}
	defer f.Close()

	downloader := s3manager.NewDownloaderWithClient(c.s3)
	_, err = downloader.DownloadWithContext(ctx, f, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// Best-effort cleanup of the partial file so the next attempt
		// doesn't get tricked by leftover bytes.
		_ = os.Remove(localPath)
		return fmt.Errorf("downloading %s/%s to %s: %w", bucket, key, localPath, err)
	}
	return nil
}

// DeleteObject removes a single object.
func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := c.s3.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("deleting %s/%s: %w", bucket, key, err)
	}
	return nil
}

// ListObjects returns every object under bucket/prefix. Empty prefix =
// every object in the bucket. Pagination is handled internally — the
// caller gets one flat slice.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	err := c.s3.ListObjectsV2PagesWithContext(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	}, func(page *s3.ListObjectsV2Output, _ bool) bool {
		for _, o := range page.Contents {
			info := ObjectInfo{}
			if o.Key != nil {
				info.Key = *o.Key
			}
			if o.Size != nil {
				info.Size = *o.Size
			}
			if o.LastModified != nil {
				info.LastModified = *o.LastModified
			}
			out = append(out, info)
		}
		return true // keep paging
	})
	if err != nil {
		return nil, fmt.Errorf("listing %s/%s: %w", bucket, prefix, err)
	}
	return out, nil
}
