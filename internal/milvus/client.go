package milvus

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/sirupsen/logrus"
)

const (
	PropertyDenyWriting = "database.force.deny.writing"
	PropertyDenyReading = "database.force.deny.reading"
)

// Client wraps the Milvus SDK client and the management HTTP client with the
// operations needed for snapshot create/restore.
type Client struct {
	// gRPC SDK client
	inner    *milvusclient.Client
	addr     string
	username string
	password string
	// management HTTP client (port 9091), derived from addr
	managementURL string
	httpClient    *http.Client
}

// NewClient connects to Milvus at addr (host:grpcPort) and returns a Client.
// The management HTTP API URL is derived automatically as http://<host>:9091.
func NewClient(ctx context.Context, addr, username, password string) (*Client, error) {
	inner, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  addr,
		Username: username,
		Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to milvus at %s: %w", addr, err)
	}
	// Derive management URL from the gRPC address: same host, port 9091.
	host := addr
	if h, _, err2 := net.SplitHostPort(addr); err2 == nil {
		host = h
	}
	return &Client{
		inner:         inner,
		addr:          addr,
		username:      username,
		password:      password,
		managementURL: "http://" + host + ":9091",
		httpClient:    &http.Client{},
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close(ctx context.Context) {
	if err := c.inner.Close(ctx); err != nil {
		logrus.WithError(err).Warn("closing milvus client")
	}
}

// ListDatabases returns all database names.
func (c *Client) ListDatabases(ctx context.Context) ([]string, error) {
	dbs, err := c.inner.ListDatabase(ctx, milvusclient.NewListDatabaseOption())
	if err != nil {
		return nil, fmt.Errorf("listing milvus databases: %w", err)
	}
	return dbs, nil
}

// FlushAll flushes all collections in every database, persisting in-memory
// segments to object storage before a snapshot is taken.
func (c *Client) FlushAll(ctx context.Context) error {
	dbs, err := c.ListDatabases(ctx)
	if err != nil {
		return err
	}
	for _, db := range dbs {
		if err := c.flushDatabase(ctx, db); err != nil {
			return fmt.Errorf("flushing database %q: %w", db, err)
		}
	}
	return nil
}

// flushDatabase flushes all collections in a single database.
// It opens a dedicated per-database connection to avoid mutating the shared client state.
func (c *Client) flushDatabase(ctx context.Context, dbName string) error {
	dbClient, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  c.addr,
		Username: c.username,
		Password: c.password,
		DBName:   dbName,
	})
	if err != nil {
		return fmt.Errorf("connecting for database %q: %w", dbName, err)
	}
	defer dbClient.Close(ctx) //nolint:errcheck

	collections, err := dbClient.ListCollections(ctx, milvusclient.NewListCollectionOption())
	if err != nil {
		return fmt.Errorf("listing collections in %q: %w", dbName, err)
	}

	for _, coll := range collections {
		log := logrus.WithFields(logrus.Fields{"db": dbName, "collection": coll})
		log.Info("flushing collection")

		task, err := dbClient.Flush(ctx, milvusclient.NewFlushOption(coll))
		if err != nil {
			return fmt.Errorf("flush %q: %w", coll, err)
		}
		if err := task.Await(ctx); err != nil {
			return fmt.Errorf("awaiting flush of %q: %w", coll, err)
		}

		log.Info("collection flushed")
	}
	return nil
}

// SetDenyWriting enables or disables forced write-denial on a database.
// Set deny=true before snapshotting; always defer deny=false to re-enable writes.
func (c *Client) SetDenyWriting(ctx context.Context, dbName string, deny bool) error {
	opt := milvusclient.NewAlterDatabasePropertiesOption(dbName).
		WithProperty(PropertyDenyWriting, deny)
	if err := c.inner.AlterDatabaseProperties(ctx, opt); err != nil {
		return fmt.Errorf("set deny.writing=%v on %q: %w", deny, dbName, err)
	}
	return nil
}

// SetDenyReading enables or disables forced read-denial on a database.
func (c *Client) SetDenyReading(ctx context.Context, dbName string, deny bool) error {
	opt := milvusclient.NewAlterDatabasePropertiesOption(dbName).
		WithProperty(PropertyDenyReading, deny)
	if err := c.inner.AlterDatabaseProperties(ctx, opt); err != nil {
		return fmt.Errorf("set deny.reading=%v on %q: %w", deny, dbName, err)
	}
	return nil
}
