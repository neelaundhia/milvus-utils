package etcd

import (
	"context"
	"fmt"
	"io"

	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Client wraps the etcd v3 client with the operations needed for snapshot
// create and restore.
type Client struct {
	inner *clientv3.Client
}

// NewClient connects to etcd at the given endpoints and returns a Client.
func NewClient(ctx context.Context, endpoints []string) (*Client, error) {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints: endpoints,
		Context:   ctx,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to etcd at %v: %w", endpoints, err)
	}
	return &Client{inner: cli}, nil
}

// Close closes the underlying etcd connection.
func (c *Client) Close() {
	if err := c.inner.Close(); err != nil {
		logrus.WithError(err).Warn("closing etcd client")
	}
}

// Snapshot streams a point-in-time etcd snapshot to w using the Maintenance
// API. The caller is responsible for closing or flushing w after this returns.
func (c *Client) Snapshot(ctx context.Context, w io.Writer) error {
	rc, err := c.inner.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("requesting etcd snapshot: %w", err)
	}
	defer rc.Close()
	if _, err := io.Copy(w, rc); err != nil {
		return fmt.Errorf("streaming etcd snapshot: %w", err)
	}
	return nil
}
