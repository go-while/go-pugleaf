package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

var appVersion = "-unset-"

func main() {
	config.AppVersion = appVersion
	log.Printf("go-pugleaf NNTP User Manager (version: %s)", config.AppVersion)
	var (
		createUser = flag.Bool("create", false, "Create a new NNTP user")
		listUsers  = flag.Bool("list", false, "List all NNTP users")
		deleteUser = flag.Bool("delete", false, "Delete an NNTP user")
		updateUser = flag.Bool("update", false, "Update an NNTP user")
		username   = flag.String("username", "random", "Username for NNTP user operations (10-20 chars)")
		password   = flag.String("password", "random", "Password for NNTP user (10-20 chars, will be bcrypt hashed)")
		maxConns   = flag.Int("maxconns", 1, "Maximum concurrent connections")
		posting    = flag.Bool("posting", false, "Allow posting (default: read-only)")
		newsgroup  = flag.String("rescan-db", "", "[TOOL] Rescan database for newsgroup (default: alt.test)")
	)
	flag.Parse()

	if !*createUser && !*listUsers && !*deleteUser && !*updateUser && *newsgroup == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s -create -username reader1 -password secret123 -maxconns 3\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -create -username poster1 -password pass456 -posting -maxconns 2\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -list\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -update -username reader1 -maxconns 5 -posting\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -delete -username reader1\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -rescan-db alt.test\n", os.Args[0])
		os.Exit(1)
	}

	if *username == "random" {
		// Generate a random username if not provided
		randomBytes := make([]byte, 6)
		if _, err := rand.Read(randomBytes); err != nil {
			log.Fatalf("Failed to generate random username: %v", err)
		}
		*username = hex.EncodeToString(randomBytes)
	}

	if *password == "random" {
		// Generate a random password if not provided
		randomBytes := make([]byte, 6)
		if _, err := rand.Read(randomBytes); err != nil {
			log.Fatalf("Failed to generate random password: %v", err)
		}
		*password = hex.EncodeToString(randomBytes)
	}

	// Initialize configuration
	//mainConfig := config.NewDefaultConfig()

	// Initialize database
	db, err := database.OpenDatabase(nil)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Shutdown()

	// Apply migrations
	if err := db.Migrate(); err != nil {
		log.Fatalf("Failed to apply database migrations: %v", err)
	}

	if *newsgroup != "" {
		// Rescan database for the specified newsgroup
		log.Printf("Rescanning database for newsgroup '%s'...", *newsgroup)
		if err := db.Rescan(*newsgroup); err != nil {
			log.Fatalf("Failed to rescan newsgroup '%s': %v", *newsgroup, err)
		}
		log.Printf("✅ Rescan completed for newsgroup '%s'", *newsgroup)
		return
	}

	switch {
	case *createUser:
		if *username == "" {
			log.Fatal("Username is required for user creation")
		}
		if *password == "" {
			log.Fatal("Password is required for user creation")
		}
		err := createNNTPUser(db, *username, *password, *maxConns, *posting)
		if err != nil {
			log.Fatalf("Failed to create NNTP user: %v", err)
		}

	case *listUsers:
		err := listNNTPUsers(db)
		if err != nil {
			log.Fatalf("Failed to list NNTP users: %v", err)
		}

	case *deleteUser:
		if *username == "" {
			log.Fatal("Username is required for user deletion")
		}
		err := deleteNNTPUser(db, *username)
		if err != nil {
			log.Fatalf("Failed to delete NNTP user: %v", err)
		}

	case *updateUser:
		if *username == "" {
			log.Fatal("Username is required for user update")
		}
		err := updateNNTPUser(db, *username, *maxConns, *posting)
		if err != nil {
			log.Fatalf("Failed to update NNTP user: %v", err)
		}
	}
}

func createNNTPUser(db *database.Database, username, password string, maxConns int, posting bool) error {
	// Validate input
	if len(username) < 10 || len(username) > 20 {
		return fmt.Errorf("username must be 10-20 characters")
	}
	if len(password) < 10 || len(password) > 20 {
		return fmt.Errorf("password must be 10-20 characters")
	}
	if maxConns < 1 || maxConns > 25 {
		return fmt.Errorf("maxconns must be between 1 and 25")
	}

	// Check if user already exists
	_, err := db.GetNNTPUserByUsername(username)
	if err == nil {
		return fmt.Errorf("NNTP user '%s' already exists", username)
	}

	// Create user
	user := &models.NNTPUser{
		Username: username,
		Password: password,
		MaxConns: maxConns,
		Posting:  posting,
		IsActive: true,
	}

	err = db.InsertNNTPUser(user)
	if err != nil {
		return fmt.Errorf("failed to insert NNTP user: %v", err)
	}

	fmt.Printf("✅ NNTP user '%s' created successfully\n", username)
	fmt.Printf("   Password: %s\n", password)
	fmt.Printf("   Max connections: %d\n", maxConns)
	fmt.Printf("   Posting allowed: %v\n", posting)

	return nil
}

func listNNTPUsers(db *database.Database) error {
	users, err := db.GetAllNNTPUsers()
	if err != nil {
		return fmt.Errorf("failed to get NNTP users: %v", err)
	}

	if len(users) == 0 {
		fmt.Println("No NNTP users found")
		return nil
	}

	fmt.Printf("Found %d NNTP users:\n\n", len(users))
	fmt.Printf("%-4s %-16s %-16s %-8s %-8s %-8s %-19s %s\n",
		"ID", "Username", "Password", "MaxConns", "Posting", "Active", "Last Login", "Created")
	fmt.Printf("%-4s %-16s %-16s %-8s %-8s %-8s %-19s %s\n",
		"----", "--------", "--------", "--------", "-------", "------", "----------", "-------")

	for _, user := range users {
		lastLogin := "Never"
		if user.LastLogin != nil {
			lastLogin = user.LastLogin.Format("2006-01-02 15:04")
		}

		fmt.Printf("%-4d %-16s %-16s %-8d %-8v %-8v %-19s %s\n",
			user.ID,
			user.Username,
			user.Password,
			user.MaxConns,
			user.Posting,
			user.IsActive,
			lastLogin,
			user.CreatedAt.Format("2006-01-02 15:04"),
		)
	}

	return nil
}

func deleteNNTPUser(db *database.Database, username string) error {
	// Check if user exists
	user, err := db.GetNNTPUserByUsername(username)
	if err != nil {
		return fmt.Errorf("NNTP user '%s' not found", username)
	}

	// Confirm deletion
	fmt.Printf("Are you sure you want to delete NNTP user '%s' (ID: %d)? [y/N]: ", username, user.ID)
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "y" && response != "yes" {
		fmt.Println("NNTP user deletion cancelled")
		return nil
	}

	// Delete user
	err = db.DeleteNNTPUser(user.ID)
	if err != nil {
		return fmt.Errorf("failed to delete NNTP user: %v", err)
	}

	fmt.Printf("✅ NNTP user '%s' deleted successfully\n", username)
	return nil
}

func updateNNTPUser(db *database.Database, username string, maxConns int, posting bool) error {
	// Check if user exists
	user, err := db.GetNNTPUserByUsername(username)
	if err != nil {
		return fmt.Errorf("NNTP user '%s' not found", username)
	}

	// Show current settings
	fmt.Printf("Current settings for '%s':\n", username)
	fmt.Printf("  Max connections: %d\n", user.MaxConns)
	fmt.Printf("  Posting allowed: %v\n", user.Posting)

	// Update user
	err = db.UpdateNNTPUserPermissions(user.ID, maxConns, posting)
	if err != nil {
		return fmt.Errorf("failed to update NNTP user: %v", err)
	}

	fmt.Printf("✅ NNTP user '%s' updated successfully\n", username)
	fmt.Printf("   Max connections: %d\n", maxConns)
	fmt.Printf("   Posting allowed: %v\n", posting)

	return nil
}
