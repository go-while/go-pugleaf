// Real NNTP provider test for go-pugleaf
package main

import (
	"fmt"
	"time"

	"github.com/go-while/go-pugleaf/internal/nntp"
)

func main() {

	// Test configuration for news.blueworldhosting.com
	config := nntp.BackendConfig{
		Host:           "news.blueworldhosting.com",
		Port:           563,
		SSL:            true,
		Username:       "", // Usually no auth required for reading
		Password:       "",
		ConnectTimeout: 30 * time.Second,
		ReadTimeout:    60 * time.Second,
		WriteTimeout:   30 * time.Second,
	}

	fmt.Printf("Testing NNTP connection to: %s:%d (SSL: %v)\n",
		config.Host, config.Port, config.SSL)

	// Create and test direct client connection
	fmt.Println("\n=== Testing Direct Client Connection ===")
	client := nntp.NewConn(&config)

	err := client.Connect()
	if err != nil {
		fmt.Printf("❌ Connection failed: %v\n", err)
		return
	}

	fmt.Printf("✅ Connected successfully to %s\n", config.Host)

	// Test basic commands
	fmt.Println("\n=== Testing NNTP Commands ===")

	// Test LIST command to get available groups (limited for testing)
	fmt.Println("1. Testing LIST command (limited to first 10 groups)...")
	groups, err := client.ListGroupsLimited(10)
	if err != nil {
		fmt.Printf("❌ LIST command failed: %v\n", err)
	} else {
		fmt.Printf("✅ LIST command successful! Found %d groups (showing first 10)\n", len(groups))
		if len(groups) > 0 {
			fmt.Printf("   Groups found:\n")
			for i, group := range groups {
				fmt.Printf("   %d. %s (count: %d, range: %d-%d, posting: %v)\n",
					i+1, group.Name, group.Count, group.First, group.Last, group.PostingOK)
			}
		}
	}

	// Test GROUP selection if we found groups
	if len(groups) > 0 {
		testGroup := groups[0].Name
		fmt.Printf("\n2. Testing GROUP selection with '%s'...\n", testGroup)

		groupInfo, err := client.SelectGroup(testGroup)
		if err != nil {
			fmt.Printf("❌ GROUP command failed: %v\n", err)
		} else {
			fmt.Printf("✅ GROUP selection successful!\n")
			fmt.Printf("   Group: %s\n", groupInfo.Name)
			fmt.Printf("   Count: %d articles\n", groupInfo.Count)
			fmt.Printf("   Range: %d - %d\n", groupInfo.First, groupInfo.Last)
			fmt.Printf("   Posting allowed: %v\n", groupInfo.PostingOK)
		}
	}
	/*
		// Close the client
		client.Pool.CloseConn(client, true)
		fmt.Println("\n=== Testing Connection Pool ===")

		// Test connection pool
		pool := nntp.NewPool(config)
		defer pool.ClosePool()

		// Start cleanup worker
		pool.StartCleanupWorker(30 * time.Second)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Get connection from pool
		poolClient, err := pool.Get(ctx)
		if err != nil {
			fmt.Printf("❌ Pool connection failed: %v\n", err)
		} else {
			fmt.Printf("✅ Pool connection successful!\n")

			// Test a quick command through pool
			exists, err := poolClient.StatArticle("<rotiv3$2j3$3@gioia.aioe.org>")
			if err != nil {
				fmt.Printf("   STAT test completed (expected error for non-existent article): %v\n", err)
			} else {
				fmt.Printf("   STAT test result: exists=%v\n", exists)
			}

			// Return to pool
			pool.Put(poolClient)
			fmt.Printf("✅ Connection returned to pool\n")
		}

		// Show pool statistics
		stats := pool.Stats()
		fmt.Printf("\nPool Statistics:\n")
		fmt.Printf("   Max connections: %d\n", stats.MaxConnections)
		fmt.Printf("   Active connections: %d\n", stats.ActiveConnections)
		fmt.Printf("   Idle connections: %d\n", stats.IdleConnections)
		fmt.Printf("   Total created: %d\n", stats.TotalCreated)
		fmt.Printf("   Total closed: %d\n", stats.TotalClosed)

		fmt.Println("\n🎉 NNTP client test completed successfully!")
		fmt.Printf("Provider %s is working with our implementation.\n", config.Host)
	*/
}
