package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alice101-dev/cloudsql-iam-tokenauth/db"

	"github.com/labstack/echo/v4"
)

type DBInfoResponse struct {
	CurrentUser string    `json:"current_user"`
	Database    string    `json:"database"`
	Version     string    `json:"version"`
	ServerTime  time.Time `json:"server_time"`
}

// loadEnv reads a simple .env file manually to keep external dependencies minimal.
// Values already present in the process environment take precedence over the file.
func loadEnv(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	// Read-only file: a Close error can't lose data, but surface it anyway.
	defer func() {
		if cerr := file.Close(); cerr != nil {
			log.Printf("closing %s: %v", filename, cerr)
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip blank lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			// Strip inline comments and surrounding whitespace from the value.
			val := strings.TrimSpace(strings.SplitN(parts[1], "#", 2)[0])
			if _, exists := os.LookupEnv(key); !exists {
				if err := os.Setenv(key, val); err != nil {
					return fmt.Errorf("setting %s from %s: %w", key, filename, err)
				}
			}
		}
	}
	return scanner.Err()
}

func main() {
	// 1. Load environment variables from the local .env file (if present).
	if err := loadEnv(".env"); err != nil {
		log.Printf("Note: could not read .env (%v); falling back to system environment variables", err)
	}

	ctx := context.Background()

	// 2. Initialize DB Connection Pool
	if err := db.Init(ctx); err != nil {
		log.Printf("Warning: Failed to connect to database: %v", err)
		log.Println("Continuing startup (the database can be checked/reconnected on demand)")
	}
	defer db.Close()

	// 3. Setup Echo Server
	e := echo.New()

	// Endpoints
	e.GET("/health", handleHealth)
	e.GET("/db-info", handleDBInfo)

	// Determine port
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Starting Echo server on port %s...", port)
	if err := e.Start(":" + port); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}

// handleHealth returns a simple health status
func handleHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status": "UP",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// handleDBInfo queries database properties to prove connectivity and identity
func handleDBInfo(c echo.Context) error {
	if db.Pool == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "Database pool is not initialized",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var resp DBInfoResponse
	query := `SELECT current_user, current_database(), version(), now();`
	err := db.Pool.QueryRow(ctx, query).Scan(
		&resp.CurrentUser,
		&resp.Database,
		&resp.Version,
		&resp.ServerTime,
	)

	if err != nil {
		// Log the full error server-side, but never leak internal details to the client.
		log.Printf("DB query failed: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to query database",
		})
	}

	return c.JSON(http.StatusOK, resp)
}
