package main

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
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
	database.NO_CACHE_BOOT = true // prevents booting caches
	log.Printf("[NNTPMGR] go-pugleaf NNTP User Manager (version: %s)", config.AppVersion)
	var (
		createUser     = flag.Bool("create", false, "Create a new NNTP user")
		listUsers      = flag.Bool("list", false, "List all NNTP users")
		deleteUser     = flag.Bool("delete", false, "Delete an NNTP user (active or not)")
		updateUser     = flag.Bool("update", false, "Update an NNTP user: changes only -maxconns and/or -posting when given")
		activateUser   = flag.Bool("activate", false, "Activate an NNTP user")
		deactivateUser = flag.Bool("deactivate", false, "Deactivate an NNTP user (logins refused, account kept)")
		setPassword    = flag.Bool("setpassword", false, "Set a new password for an NNTP user (uses -password)")
		username       = flag.String("username", "random", "Username for NNTP user operations (10-20 chars; 'random' only with -create)")
		password       = flag.String("password", "random", "Password for -create/-setpassword (10-72 bytes, bcrypt hashed; 'random' generates and prints one)")
		maxConns       = flag.Int("maxconns", database.DefaultNNTPUserMaxConns, "Maximum concurrent connections (1-25)")
		posting        = flag.Bool("posting", false, "Allow posting (default: read-only; -posting=false revokes it with -update)")
		dataDir        = flag.String("data", "./data", "Directory to store database files")
	)
	flag.Parse()
	set := flagsSet()

	actions := 0
	for _, a := range []bool{*createUser, *listUsers, *deleteUser, *updateUser, *activateUser, *deactivateUser, *setPassword} {
		if a {
			actions++
		}
	}
	if actions == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s -create -username reader0001 -password secret1234 -maxconns 3\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -create -username poster0001 -password random -posting -maxconns 2\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -list\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -update -username reader0001 -maxconns 5 -posting\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -update -username reader0001 -posting=false\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -deactivate -username reader0001\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -activate -username reader0001\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -setpassword -username reader0001 -password random\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -delete -username reader0001\n", os.Args[0])
		os.Exit(1)
	}
	if actions > 1 {
		log.Fatal("[NNTPMGR] give only one of -create, -list, -update, -activate, -deactivate, -setpassword, -delete")
	}

	if *createUser && *username == "random" {
		// Generate a random username if not provided
		randomBytes := make([]byte, 6)
		if _, err := rand.Read(randomBytes); err != nil {
			log.Fatalf("[NNTPMGR] Failed to generate random username: %v", err)
		}
		*username = hex.EncodeToString(randomBytes)
	}
	if !*createUser && !*listUsers && !set["username"] {
		log.Fatal("[NNTPMGR] -username is required for this operation")
	}

	// The password is printed only when it was generated here (OPS-2).
	generatedPassword := false
	if *createUser || *setPassword {
		if *password == "random" {
			randomBytes := make([]byte, 6)
			if _, err := rand.Read(randomBytes); err != nil {
				log.Fatalf("[NNTPMGR] Failed to generate random password: %v", err)
			}
			*password = hex.EncodeToString(randomBytes)
			generatedPassword = true
		}
		// One password rule for -create and -setpassword; 72 bytes is the bcrypt limit.
		if len(*password) < 10 || len(*password) > 72 {
			log.Fatal("[NNTPMGR] password must be 10-72 bytes")
		}
	}

	// Initialize database
	dbConfig := database.DefaultDBConfig()
	dbConfig.DataDir = *dataDir

	db, err := database.OpenDatabase(dbConfig)
	if err != nil {
		log.Fatalf("[NNTPMGR] Failed to initialize database: %v", err)
	}

	switch {
	case *createUser:
		if err = createNNTPUser(db, *username, *password, generatedPassword, *maxConns, *posting); err != nil {
			err = fmt.Errorf("failed to create NNTP user: %w", err)
		}
	case *listUsers:
		if err = listNNTPUsers(db); err != nil {
			err = fmt.Errorf("failed to list NNTP users: %w", err)
		}
	case *deleteUser:
		if err = deleteNNTPUser(db, *username); err != nil {
			err = fmt.Errorf("failed to delete NNTP user: %w", err)
		}
	case *updateUser:
		if err = updateNNTPUser(db, *username, set, *maxConns, *posting); err != nil {
			err = fmt.Errorf("failed to update NNTP user: %w", err)
		}
	case *activateUser, *deactivateUser:
		if err = setNNTPUserActive(db, *username, *activateUser); err != nil {
			err = fmt.Errorf("failed to change NNTP user state: %w", err)
		}
	case *setPassword:
		if err = setNNTPUserPassword(db, *username, *password, generatedPassword); err != nil {
			err = fmt.Errorf("failed to set NNTP user password: %w", err)
		}
	}

	if shutdownErr := db.Shutdown(); shutdownErr != nil {
		log.Printf("[NNTPMGR] database shutdown: %v", shutdownErr)
	}
	if err != nil {
		log.Fatalf("[NNTPMGR] %v", err)
	}
}

// flagsSet returns the names of the flags given on the command line.
func flagsSet() map[string]bool {
	set := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func createNNTPUser(db *database.Database, username, password string, generatedPassword bool, maxConns int, posting bool) error {
	// Validate input (the password rule is checked in main)
	if len(username) < 10 || len(username) > 20 {
		return fmt.Errorf("username must be 10-20 characters")
	}
	if maxConns < 1 || maxConns > 25 {
		return fmt.Errorf("maxconns must be between 1 and 25")
	}

	// Check if user already exists (active or not: the username is UNIQUE)
	_, err := db.GetNNTPUserByUsernameAnyState(username)
	if err == nil {
		return fmt.Errorf("NNTP user '%s' already exists", username)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lookup of '%s': %w", username, err)
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
	log.Printf("[NNTPMGR] created NNTP user '%s' (maxconns=%d posting=%v)", username, maxConns, posting)

	fmt.Printf("✅ NNTP user '%s' created successfully\n", username)
	if generatedPassword {
		fmt.Printf("   Password: %s\n", password)
	} else {
		fmt.Printf("   Password: (as given)\n")
	}
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

	// Password hashes are never printed (OPS-2).
	fmt.Printf("Found %d NNTP users:\n\n", len(users))
	fmt.Printf("%-4s %-20s %-8s %-8s %-8s %-19s %s\n",
		"ID", "Username", "MaxConns", "Posting", "Active", "Last Login", "Created")
	fmt.Printf("%-4s %-20s %-8s %-8s %-8s %-19s %s\n",
		"----", "--------", "--------", "-------", "------", "----------", "-------")

	for _, user := range users {
		lastLogin := "Never"
		if user.LastLogin != nil {
			lastLogin = user.LastLogin.Format("2006-01-02 15:04")
		}

		fmt.Printf("%-4d %-20s %-8d %-8v %-8v %-19s %s\n",
			user.ID,
			user.Username,
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
	// Check if user exists (active or not)
	user, err := db.GetNNTPUserByUsernameAnyState(username)
	if err != nil {
		return fmt.Errorf("NNTP user '%s' not found: %w", username, err)
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
	log.Printf("[NNTPMGR] deleted NNTP user '%s' (ID: %d)", username, user.ID)

	fmt.Printf("✅ NNTP user '%s' deleted successfully\n", username)
	return nil
}

// updateNNTPUser changes only the settings whose flags were given (set): -maxconns
// and/or -posting. Without either it changes nothing and returns an error.
func updateNNTPUser(db *database.Database, username string, set map[string]bool, maxConns int, posting bool) error {
	if !set["maxconns"] && !set["posting"] {
		return fmt.Errorf("nothing to update: give -maxconns and/or -posting")
	}
	if set["maxconns"] && (maxConns < 1 || maxConns > 25) {
		return fmt.Errorf("maxconns must be between 1 and 25")
	}

	// Check if user exists (active or not)
	user, err := db.GetNNTPUserByUsernameAnyState(username)
	if err != nil {
		return fmt.Errorf("NNTP user '%s' not found: %w", username, err)
	}

	// Show current settings
	fmt.Printf("Current settings for '%s':\n", username)
	fmt.Printf("  Max connections: %d\n", user.MaxConns)
	fmt.Printf("  Posting allowed: %v\n", user.Posting)

	newMaxConns, newPosting := user.MaxConns, user.Posting
	if set["maxconns"] {
		newMaxConns = maxConns
	}
	if set["posting"] {
		newPosting = posting
	}

	// Update user
	err = db.UpdateNNTPUserPermissions(user.ID, newMaxConns, newPosting)
	if err != nil {
		return fmt.Errorf("failed to update NNTP user: %v", err)
	}
	log.Printf("[NNTPMGR] updated NNTP user '%s' (maxconns=%d posting=%v)", username, newMaxConns, newPosting)

	fmt.Printf("✅ NNTP user '%s' updated successfully\n", username)
	fmt.Printf("   Max connections: %d\n", newMaxConns)
	fmt.Printf("   Posting allowed: %v\n", newPosting)

	return nil
}

// setNNTPUserActive activates or deactivates an NNTP user (looked up in any state).
func setNNTPUserActive(db *database.Database, username string, active bool) error {
	user, err := db.GetNNTPUserByUsernameAnyState(username)
	if err != nil {
		return fmt.Errorf("NNTP user '%s' not found: %w", username, err)
	}
	state := "deactivated"
	if active {
		state = "activated"
		err = db.ActivateNNTPUser(user.ID)
	} else {
		err = db.DeactivateNNTPUser(user.ID)
	}
	if err != nil {
		return err
	}
	log.Printf("[NNTPMGR] %s NNTP user '%s' (ID: %d)", state, username, user.ID)
	fmt.Printf("✅ NNTP user '%s' %s\n", username, state)
	return nil
}

// setNNTPUserPassword stores a new password; it is printed only when it was generated.
func setNNTPUserPassword(db *database.Database, username, password string, generatedPassword bool) error {
	user, err := db.GetNNTPUserByUsernameAnyState(username)
	if err != nil {
		return fmt.Errorf("NNTP user '%s' not found: %w", username, err)
	}
	if err := db.UpdateNNTPUserPassword(user.ID, password); err != nil {
		return err
	}
	log.Printf("[NNTPMGR] new password set for NNTP user '%s' (ID: %d)", username, user.ID)
	fmt.Printf("✅ Password of NNTP user '%s' changed\n", username)
	if generatedPassword {
		fmt.Printf("   Password: %s\n", password)
	} else {
		fmt.Printf("   Password: (as given)\n")
	}
	if !user.IsActive {
		fmt.Printf("   Note: the user is inactive (use -activate)\n")
	}
	return nil
}
