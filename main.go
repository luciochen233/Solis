package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"

	"Solis/internal/config"
	"Solis/internal/db"
	"Solis/internal/server"
)

func main() {
	configPath := flag.String("config", "config.toml", "Path to config file")
	hashPassword := flag.String("hash-password", "", "Generate a bcrypt hash of the given password and exit")
	flag.StringVar(configPath, "c", "config.toml", "Path to config file (shorthand)")
	flag.Parse()

	// If the user requested to hash a password, hash it and print the result
	if *hashPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*hashPassword), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("Error hashing password: %v", err)
		}
		fmt.Printf("Bcrypt hash for password %q:\n%s\n", *hashPassword, string(hash))
		os.Exit(0)
	}

	// Out-of-the-box ease: check if config file exists, if not, attempt to copy it from config.toml.example
	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		if *configPath == "config.toml" {
			if _, errExample := os.Stat("config.toml.example"); errExample == nil {
				log.Println("config.toml not found, copying from config.toml.example...")
				input, errRead := os.ReadFile("config.toml.example")
				if errRead == nil {
					errWrite := os.WriteFile("config.toml", input, 0644)
					if errWrite != nil {
						log.Printf("Warning: failed to create config.toml from example: %v", errWrite)
					}
				}
			}
		}
	}

	// Load configuration
	log.Printf("Loading configuration from %s...", *configPath)
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v\n\nIf the config file does not exist, make sure to create config.toml (you can use config.toml.example as a template).", err)
	}

	// Initialize the CGO-free database
	log.Printf("Initializing database at %s...", cfg.Database.Path)
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		}
	}()

	// Ensure upload directory exists before starting server
	if err := os.MkdirAll(cfg.Upload.Dir, 0755); err != nil {
		log.Fatalf("Failed to create upload directory: %v", err)
	}

	// Initialize and start the premium Solis server
	srv := server.New(cfg, database)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server stopped with error: %v", err)
	}
}
