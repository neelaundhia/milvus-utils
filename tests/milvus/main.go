// Ad-hoc Milvus client test.
//
// Port-forward Milvus gRPC and management API before running:
//
//	kubectl port-forward svc/<operator-name>-milvus 19530:19530 9091:9091
//
// Run:
//
//	go run ./tests/milvus
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/neelaundhia/milvus-utils/internal/milvus"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	addr := "localhost:19530"
	username := ""
	password := ""
	// Use the first non-default database for deny tests, or "default" if none exist.
	testDB := "default"

	fmt.Printf("--- connecting to Milvus at %s\n", addr)
	c, err := milvus.NewClient(ctx, addr, username, password)
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	defer c.Close(ctx)
	fmt.Println("PASS NewClient / Close")

	// ListDatabases
	fmt.Println("--- ListDatabases")
	dbs, err := c.ListDatabases(ctx)
	if err != nil {
		log.Fatalf("ListDatabases: %v", err)
	}
	fmt.Printf("PASS ListDatabases: %v\n", dbs)
	if len(dbs) > 0 {
		testDB = dbs[0]
	}

	// SetDenyWriting — enable then immediately disable
	fmt.Printf("--- SetDenyWriting(true) on %q\n", testDB)
	if err := c.SetDenyWriting(ctx, testDB, true); err != nil {
		log.Fatalf("SetDenyWriting(true): %v", err)
	}
	fmt.Println("PASS SetDenyWriting(true)")

	fmt.Printf("--- SetDenyWriting(false) on %q\n", testDB)
	if err := c.SetDenyWriting(ctx, testDB, false); err != nil {
		log.Fatalf("SetDenyWriting(false): %v", err)
	}
	fmt.Println("PASS SetDenyWriting(false)")

	// SetDenyReading — enable then immediately disable
	fmt.Printf("--- SetDenyReading(true) on %q\n", testDB)
	if err := c.SetDenyReading(ctx, testDB, true); err != nil {
		log.Fatalf("SetDenyReading(true): %v", err)
	}
	fmt.Println("PASS SetDenyReading(true)")

	fmt.Printf("--- SetDenyReading(false) on %q\n", testDB)
	if err := c.SetDenyReading(ctx, testDB, false); err != nil {
		log.Fatalf("SetDenyReading(false): %v", err)
	}
	fmt.Println("PASS SetDenyReading(false)")

	// PauseGC + ResumeGC round-trip
	fmt.Println("--- PauseGC(30)")
	ticket, err := c.PauseGC(ctx, 30)
	if err != nil {
		log.Fatalf("PauseGC: %v", err)
	}
	fmt.Printf("PASS PauseGC: ticket=%q\n", ticket)

	fmt.Println("--- ResumeGC")
	if err := c.ResumeGC(ctx, ticket); err != nil {
		log.Fatalf("ResumeGC: %v", err)
	}
	fmt.Println("PASS ResumeGC")

	// FlushAll
	fmt.Println("--- FlushAll")
	if err := c.FlushAll(ctx); err != nil {
		log.Fatalf("FlushAll: %v", err)
	}
	fmt.Println("PASS FlushAll")

	fmt.Println("\nAll checks passed.")
}
