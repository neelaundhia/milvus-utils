// Ad-hoc etcd client test.
//
// Port-forward etcd before running:
//
//	kubectl port-forward svc/<operator-name>-etcd 2379:2379
//
// Run:
//
//	go run ./tests/etcd
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/neelaundhia/milvus-utils/internal/etcd"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	endpoint := "localhost:2379"
	fmt.Printf("--- connecting to etcd at %s\n", endpoint)

	c, err := etcd.NewClient(ctx, []string{endpoint})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	fmt.Println("PASS NewClient / Close")

	// Snapshot
	fmt.Println("--- Snapshot: streaming snapshot to buffer")
	var buf bytes.Buffer
	if err := c.Snapshot(ctx, &buf); err != nil {
		log.Fatalf("Snapshot: %v", err)
	}
	fmt.Printf("PASS Snapshot: received %d bytes\n", buf.Len())
}
