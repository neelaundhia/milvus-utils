package s3

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// maxDeleteBatch is the maximum number of objects per DeleteObjects API call.
const maxDeleteBatch = 1000

// CopyPrefix performs a parallel server-side copy of all objects from
// srcBucket/srcPrefix to dstBucket/dstPrefix, preserving the relative key
// structure. Returns the number of objects copied.
//
// Each individual object must be ≤ 5 GiB (S3 CopyObject limit). Milvus
// segment files are typically well under this threshold.
func (c *Client) CopyPrefix(ctx context.Context, srcBucket, srcPrefix, dstBucket, dstPrefix string) (int, error) {
	keys, err := c.ListObjects(ctx, srcBucket, srcPrefix)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		logrus.Warn("no objects found to copy")
		return 0, nil
	}

	logrus.WithFields(logrus.Fields{
		"src":   fmt.Sprintf("s3://%s/%s", srcBucket, srcPrefix),
		"dst":   fmt.Sprintf("s3://%s/%s", dstBucket, dstPrefix),
		"count": len(keys),
	}).Info("starting parallel S3 copy")

	var copied atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.concurrency)

	for _, key := range keys {
		g.Go(func() error {
			relKey := strings.TrimPrefix(key, srcPrefix)
			dstKey := dstPrefix + relKey
			copySource := srcBucket + "/" + key

			_, err := c.inner.CopyObject(gctx, &s3.CopyObjectInput{
				Bucket:     aws.String(dstBucket),
				CopySource: aws.String(copySource),
				Key:        aws.String(dstKey),
			})
			if err != nil {
				return fmt.Errorf("copying s3://%s/%s → s3://%s/%s: %w",
					srcBucket, key, dstBucket, dstKey, err)
			}
			if n := copied.Add(1); n%1000 == 0 {
				logrus.WithField("copied", n).Info("S3 copy progress")
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return 0, err
	}

	total := int(copied.Load())
	logrus.WithField("count", total).Info("S3 copy complete")
	return total, nil
}

// DeletePrefix deletes all objects under bucket/prefix using parallel batch
// deletions. Returns the number of objects deleted.
func (c *Client) DeletePrefix(ctx context.Context, bucket, prefix string) (int, error) {
	keys, err := c.ListObjects(ctx, bucket, prefix)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}

	logrus.WithFields(logrus.Fields{
		"prefix": fmt.Sprintf("s3://%s/%s", bucket, prefix),
		"count":  len(keys),
	}).Info("starting parallel S3 delete")

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.concurrency)

	for i := 0; i < len(keys); i += maxDeleteBatch {
		batch := keys[i:min(i+maxDeleteBatch, len(keys))]
		g.Go(func() error {
			objects := make([]types.ObjectIdentifier, len(batch))
			for j, key := range batch {
				objects[j] = types.ObjectIdentifier{Key: aws.String(key)}
			}
			_, err := c.inner.DeleteObjects(gctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &types.Delete{
					Objects: objects,
					Quiet:   aws.Bool(true),
				},
			})
			if err != nil {
				return fmt.Errorf("deleting batch from s3://%s/%s: %w", bucket, prefix, err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return 0, err
	}

	logrus.WithField("count", len(keys)).Info("S3 delete complete")
	return len(keys), nil
}
