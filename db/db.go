package db

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2/google"
)

// Pool is the global database connection pool
var Pool *pgxpool.Pool

// Init initializes the database connection pool using IAM authentication
func Init(ctx context.Context) error {
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	user := os.Getenv("DB_USER")
	dbname := os.Getenv("DB_NAME")
	sslmode := os.Getenv("DB_SSLMODE")

	if host == "" || user == "" || dbname == "" {
		return fmt.Errorf("missing required environment variables (DB_HOST, DB_USER, DB_NAME)")
	}
	if port == "" {
		port = "5432"
	}
	if sslmode == "" {
		sslmode = "require"
	}

	log.Printf("Initializing DB connection to %s:%s for user %s with sslmode=%s", host, port, user, sslmode)

	// 1. Initialize Google Application Default Credentials with sqlservice.login scope
	credentials, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/sqlservice.login")
	if err != nil {
		return fmt.Errorf("failed to load application default credentials: %w. Make sure you run 'gcloud auth application-default login'", err)
	}
	tokenSource := credentials.TokenSource

	// 2. Build the database connection URL
	// We escape the user because email addresses contain '@' and '.'
	connStr := fmt.Sprintf("postgres://%s@%s:%s/%s?sslmode=%s",
		url.QueryEscape(user),
		host,
		port,
		dbname,
		sslmode,
	)

	config, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return fmt.Errorf("failed to parse connection string config: %w", err)
	}

	// 3. Set the BeforeConnect hook to dynamically inject the token as the password.
	// pgx pool calls BeforeConnect before establishing any new connection.
	config.BeforeConnect = func(ctx context.Context, cfg *pgx.ConnConfig) error {
		// Fetch a fresh OAuth2 token (tokenSource automatically caches and refreshes it)
		tok, err := tokenSource.Token()
		if err != nil {
			return fmt.Errorf("failed to fetch IAM OAuth2 token: %w", err)
		}

		// Set the token as the password
		cfg.Password = tok.AccessToken
		log.Printf("Connecting with dynamic IAM token (Expires: %s)", tok.Expiry.Format("15:04:05 MST"))
		return nil
	}

	// 4. Create the connection pool
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to create pgx pool: %w", err)
	}

	// 5. Test the connection to ensure IAM auth works
	log.Println("Pinging database to verify IAM credentials...")
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("failed to ping database: %w", err)
	}

	Pool = pool
	log.Println("Database connection pool successfully verified and initialized!")
	return nil
}

// Close closes the database connection pool
func Close() {
	if Pool != nil {
		Pool.Close()
		log.Println("Database connection pool closed")
	}
}
