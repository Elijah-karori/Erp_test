package env

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// EnvConfig holds validated startup environment values
type EnvConfig struct {
	AppEnv        string
	DatabaseURL   string
	JWTSecret     string
	SMTPHost      string
	SMTPPort      int
	SMTPUser      string
	SMTPPass      string
	SMTPSender    string
	SMTPAvailable bool
}

// ValidateAndLoad performs strict validation of the environment variables at startup,
// preventing silent degradations (like insecure defaults in production) and providing
// clear, operator-friendly error and warning reports.
func ValidateAndLoad() *EnvConfig {
	cfg := &EnvConfig{}

	// 1. Get APP_ENV (defaults to development)
	cfg.AppEnv = strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if cfg.AppEnv == "" {
		cfg.AppEnv = "development"
	}

	// 2. Validate DATABASE_URL (or fallback SUPABASE_DB_URL)
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		dbURL = strings.TrimSpace(os.Getenv("SUPABASE_DB_URL"))
	}

	if dbURL == "" {
		log.Fatalf("\n============================================================\n" +
			"ERROR: DATABASE CONFIGURATION MISSING\n" +
			"============================================================\n" +
			"No database configured. Set DATABASE_URL to a valid Postgres connection string.\n" +
			"Example:\n" +
			"  export DATABASE_URL=\"postgres://user:pass@host:5432/dbname?sslmode=disable\"\n" +
			"============================================================\n")
	}

	// Validate DB URL scheme
	parsedURL, err := url.Parse(dbURL)
	if err != nil || (parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql") {
		log.Fatalf("\n============================================================\n" +
			"ERROR: INVALID DATABASE CONNECTION STRING FORMAT\n" +
			"============================================================\n" +
			"The provided database connection string has an invalid scheme: %s\n" +
			"DATABASE_URL must start with 'postgres://' or 'postgresql://'.\n" +
			"============================================================\n", dbURL)
	}
	cfg.DatabaseURL = dbURL

	// 3. Validate ERP_JWT_SECRET
	secret := os.Getenv("ERP_JWT_SECRET")
	if cfg.AppEnv == "production" {
		if secret == "" || secret == "dev-only-insecure-secret-change-me" {
			log.Fatalf("\n============================================================\n" +
				"FATAL ERROR: INSECURE JWT SECRET DETECTED IN PRODUCTION\n" +
				"============================================================\n" +
				"ERP_JWT_SECRET is required to be set and secure outside development.\n" +
				"Please generate a cryptographically secure key and configure it.\n\n" +
				"You can generate a secure secret using:\n" +
				"  openssl rand -hex 32\n" +
				"============================================================\n")
		}
	} else {
		if secret == "" {
			fmt.Print("\n============================================================\n" +
				"WARNING: ERP_JWT_SECRET IS UNSET\n" +
				"============================================================\n" +
				"ERP_JWT_SECRET is unset in development mode. Falling back to insecure key.\n" +
				"Ensure a secure secret is configured in staging and production!\n" +
				"============================================================\n\n")
			secret = "dev-only-insecure-secret-change-me"
		}
	}
	cfg.JWTSecret = secret

	// 4. Validate SMTP configurations
	cfg.SMTPHost = os.Getenv("SMTP_HOST")
	if cfg.SMTPHost == "" {
		cfg.SMTPHost = "smtp.gmail.com"
	}

	portStr := os.Getenv("SMTP_PORT")
	if portStr == "" {
		cfg.SMTPPort = 587
	} else {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			log.Fatalf("\n============================================================\n" +
				"ERROR: INVALID SMTP PORT CONFIGURATION\n" +
				"============================================================\n" +
				"SMTP_PORT must be a valid numeric port. Received: '%s'\n" +
				"============================================================\n", portStr)
		}
		cfg.SMTPPort = p
	}

	cfg.SMTPUser = os.Getenv("SMTP_USER")
	cfg.SMTPPass = os.Getenv("SMTP_PASS")
	cfg.SMTPSender = os.Getenv("SMTP_SENDER")
	if cfg.SMTPSender == "" {
		if cfg.SMTPUser != "" {
			cfg.SMTPSender = cfg.SMTPUser
		} else {
			cfg.SMTPSender = "no-reply@sme-kenya-erp.test"
		}
	}

	if cfg.SMTPUser != "" && cfg.SMTPPass != "" {
		cfg.SMTPAvailable = true
	} else {
		cfg.SMTPAvailable = false
		if cfg.AppEnv == "production" {
			fmt.Print("\n############################################################\n" +
				"   🚨 WARNING: GMAIL SMTP CREDENTIALS ARE MISSING 🚨   \n" +
				"############################################################\n" +
				"Production environment detected, but SMTP_USER or SMTP_PASS is unset!\n" +
				"The ERP email invitation and activation alerts will run in MOCK MODE,\n" +
				"only writing emails to console logs instead of sending them.\n" +
				"Configure your Gmail SMTP credentials immediately to enable invites.\n" +
				"############################################################\n\n")
		} else {
			fmt.Println("[Environment check] SMTP user/password is missing — running in mock console email mode.")
		}
	}

	return cfg
}
