package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/go-while/go-pugleaf/internal/config"
	"github.com/go-while/go-pugleaf/internal/database"
	"github.com/go-while/go-pugleaf/internal/models"
)

var appVersion = "-"

func main() {
	var (
		createUser = flag.Bool("create", false, "Create a new user")
		listUsers  = flag.Bool("list", false, "List all users")
		deleteUser = flag.Bool("delete", false, "Delete a user")
		updateUser = flag.Bool("update", false, "Update a user's password")
		username   = flag.String("username", "", "Username for user operations")
		email      = flag.String("email", "", "Email for user creation")
		display    = flag.String("display", "", "Display name for user creation")
		admin      = flag.Bool("admin", false, "Grant admin permissions to user")
	)
	flag.Parse()

	if !*createUser && !*listUsers && !*deleteUser && !*updateUser {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s -create -username john -email john@example.com -display \"John Doe\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -list\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -update -username john\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -delete -username john\n", os.Args[0])
		os.Exit(1)
	}

	// Initialize configuration
	mainConfig := config.NewDefaultConfig()
	appVersion = mainConfig.AppVersion
	log.Printf("go-pugleaf User Manager (version: %s)", appVersion)

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

	switch {
	case *createUser:
		if *username == "" {
			log.Fatal("Username is required for user creation")
		}
		if *email == "" {
			log.Fatal("Email is required for user creation")
		}
		err := createNewUser(db, *username, *email, *display, *admin)
		if err != nil {
			log.Fatalf("Failed to create user: %v", err)
		}

	case *listUsers:
		err := listAllUsers(db)
		if err != nil {
			log.Fatalf("Failed to list users: %v", err)
		}

	case *deleteUser:
		if *username == "" {
			log.Fatal("Username is required for user deletion")
		}
		err := deleteExistingUser(db, *username)
		if err != nil {
			log.Fatalf("Failed to delete user: %v", err)
		}

	case *updateUser:
		if *username == "" {
			log.Fatal("Username is required for user update")
		}
		err := updateUserPassword(db, *username)
		if err != nil {
			log.Fatalf("Failed to update user: %v", err)
		}
	}
}

func createNewUser(db *database.Database, username, email, displayName string, isAdmin bool) error {
	// Check if user already exists
	_, err := db.GetUserByUsername(username)
	if err == nil {
		return fmt.Errorf("user '%s' already exists", username)
	}

	// Check if email already exists
	_, err = db.GetUserByEmail(email)
	if err == nil {
		return fmt.Errorf("email '%s' already exists", email)
	}

	// Get password
	fmt.Print("Enter password: ")
	password, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return fmt.Errorf("failed to read password: %v", err)
	}
	fmt.Println()

	fmt.Print("Confirm password: ")
	confirmPassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return fmt.Errorf("failed to read password confirmation: %v", err)
	}
	fmt.Println()

	if string(password) != string(confirmPassword) {
		return fmt.Errorf("passwords do not match")
	}

	if len(password) < 6 {
		return fmt.Errorf("password must be at least 6 characters long")
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %v", err)
	}

	// Set display name to username if not provided
	if displayName == "" {
		displayName = username
	}

	// Create user
	user := &models.User{
		Username:     username,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: string(hashedPassword),
	}

	err = db.InsertUser(user)
	if err != nil {
		return fmt.Errorf("failed to insert user: %v", err)
	}

	fmt.Printf("✅ User '%s' created successfully\n", username)

	// Add admin permission if requested
	if isAdmin {
		// Note: This would require implementing user permissions
		// For now, just log that admin permissions were requested
		fmt.Printf("⚠️  Admin permissions requested but not yet implemented\n")
	}

	return nil
}

func listAllUsers(db *database.Database) error {
	users, err := db.GetAllUsers()
	if err != nil {
		return fmt.Errorf("failed to get users: %v", err)
	}

	if len(users) == 0 {
		fmt.Println("No users found")
		return nil
	}

	fmt.Printf("Found %d users:\n\n", len(users))
	fmt.Printf("%-4s %-20s %-30s %-20s %s\n", "ID", "Username", "Email", "Display Name", "Created")
	fmt.Printf("%-4s %-20s %-30s %-20s %s\n", "----", "--------", "-----", "------------", "-------")

	for _, user := range users {
		fmt.Printf("%-4d %-20s %-30s %-20s %s\n",
			user.ID,
			truncate(user.Username, 20),
			truncate(user.Email, 30),
			truncate(user.DisplayName, 20),
			user.CreatedAt.Format("2006-01-02 15:04"),
		)
	}

	return nil
}

func deleteExistingUser(db *database.Database, username string) error {
	// Check if user exists
	user, err := db.GetUserByUsername(username)
	if err != nil {
		return fmt.Errorf("user '%s' not found", username)
	}

	// Confirm deletion
	fmt.Printf("Are you sure you want to delete user '%s' (ID: %d)? [y/N]: ", username, user.ID)
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "y" && response != "yes" {
		fmt.Println("User deletion cancelled")
		return nil
	}

	// Note: This would require implementing user deletion
	// For now, just show what would be deleted
	fmt.Printf("⚠️  User deletion not yet implemented\n")
	fmt.Printf("Would delete: %s (ID: %d, Email: %s)\n", user.Username, user.ID, user.Email)

	return nil
}

func updateUserPassword(db *database.Database, username string) error {
	// Check if user exists
	user, err := db.GetUserByUsername(username)
	if err != nil {
		return fmt.Errorf("user '%s' not found", username)
	}

	// Get new password
	fmt.Printf("Enter new password for '%s': ", username)
	password, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return fmt.Errorf("failed to read password: %v", err)
	}
	fmt.Println()

	fmt.Print("Confirm new password: ")
	confirmPassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return fmt.Errorf("failed to read password confirmation: %v", err)
	}
	fmt.Println()

	if string(password) != string(confirmPassword) {
		return fmt.Errorf("passwords do not match")
	}

	if len(password) < 6 {
		return fmt.Errorf("password must be at least 6 characters long")
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %v", err)
	}

	// Update password
	err = db.UpdateUserPassword(int64(user.ID), string(hashedPassword))
	if err != nil {
		return fmt.Errorf("failed to update password: %v", err)
	}

	fmt.Printf("✅ Password updated successfully for user '%s'\n", username)

	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
