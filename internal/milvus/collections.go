package milvus

import (
	"context"
	"fmt"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// ListCollections returns all collection names in the given database.
// It opens a dedicated per-database connection to avoid mutating the shared client state.
func (c *Client) ListCollections(ctx context.Context, dbName string) ([]string, error) {
	dbClient, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  c.addr,
		Username: c.username,
		Password: c.password,
		DBName:   dbName,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting for database %q: %w", dbName, err)
	}
	defer dbClient.Close(ctx) //nolint:errcheck

	collections, err := dbClient.ListCollections(ctx, milvusclient.NewListCollectionOption())
	if err != nil {
		return nil, fmt.Errorf("listing collections in %q: %w", dbName, err)
	}
	return collections, nil
}
