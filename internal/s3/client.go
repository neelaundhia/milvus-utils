package s3

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sirupsen/logrus"
)

const defaultConcurrency = 64

// Client wraps the AWS S3 client with the operations needed for snapshot
// create and restore.
type Client struct {
	inner       *s3.Client
	tm          *transfermanager.Client
	concurrency int
}

// NewClient creates an S3 client using the default AWS credential chain
// (IRSA in EKS, env vars, shared config, etc.).
func NewClient(ctx context.Context) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	inner := s3.NewFromConfig(cfg)
	return &Client{
		inner:       inner,
		tm:          transfermanager.New(inner),
		concurrency: defaultConcurrency,
	}, nil
}

// ParseBucketURI strips the "s3://" prefix from a bucket URI and returns the
// bucket name. If no prefix is present the input is returned as-is.
func ParseBucketURI(uri string) string {
	return strings.TrimPrefix(uri, "s3://")
}

// ListObjects returns all object keys under prefix in the given bucket.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string) ([]string, error) {
	paginator := s3.NewListObjectsV2Paginator(c.inner, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	var keys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing objects in s3://%s/%s: %w", bucket, prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}
	return keys, nil
}

// ListCommonPrefixes returns the "directory" prefixes directly under prefix,
// using delimiter (typically "/") to group keys.
func (c *Client) ListCommonPrefixes(ctx context.Context, bucket, prefix, delimiter string) ([]string, error) {
	paginator := s3.NewListObjectsV2Paginator(c.inner, &s3.ListObjectsV2Input{
		Bucket:    aws.String(bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String(delimiter),
	})
	var prefixes []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing prefixes in s3://%s/%s: %w", bucket, prefix, err)
		}
		for _, p := range page.CommonPrefixes {
			prefixes = append(prefixes, *p.Prefix)
		}
	}
	return prefixes, nil
}

// Upload writes body to s3://bucket/key using the transfer manager, which
// automatically handles multipart upload for large files.
func (c *Client) Upload(ctx context.Context, bucket, key string, body io.Reader) error {
	logrus.WithFields(logrus.Fields{"bucket": bucket, "key": key}).Info("uploading to S3")
	_, err := c.tm.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("uploading to s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}

// Download streams the object at s3://bucket/key into w.
func (c *Client) Download(ctx context.Context, bucket, key string, w io.Writer) error {
	logrus.WithFields(logrus.Fields{"bucket": bucket, "key": key}).Info("downloading from S3")
	result, err := c.inner.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("downloading s3://%s/%s: %w", bucket, key, err)
	}
	defer result.Body.Close()
	if _, err := io.Copy(w, result.Body); err != nil {
		return fmt.Errorf("streaming s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}
