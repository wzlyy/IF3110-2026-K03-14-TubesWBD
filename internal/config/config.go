package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// PlatformDBConfig holds PostgreSQL connection settings for the platform database.
type PlatformDBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// DSN builds the standard PostgreSQL connection string.
func (c *PlatformDBConfig) DSN() string {
	u := &url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   c.DBName,
	}

	if c.Password != "" {
		u.User = url.UserPassword(c.User, c.Password)
	} else if c.User != "" {
		u.User = url.User(c.User)
	}

	q := u.Query()
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	q.Set("sslmode", sslMode)
	u.RawQuery = q.Encode()

	return u.String()
}

// Config represents all application configuration parameters.
type Config struct {
	Port                 string
	AppEnv               string
	EncryptionKey        string
	AdminInitialUsername string
	AdminInitialPassword string
	PlatformDB           PlatformDBConfig
	CookieSecure         bool
}

// IsProduction returns true if the current environment is production.
func (c *Config) IsProduction() bool {
	return c.AppEnv == "production"
}

// Load loads configuration from environment variables (and optional .env file).
func Load() (*Config, error) {
	// Attempt to load .env file from current or parent directories
	envFiles := []string{".env", "../.env", "../../.env"}
	for _, f := range envFiles {
		if _, err := os.Stat(f); err == nil {
			_ = godotenv.Load(f)
			break
		}
	}

	port := getEnv("PORT", "8080")
	appEnv := getEnv("APP_ENV", "development")

	// Encryption Key Validation (AES-256 requires exactly 32 bytes)
	encKey := os.Getenv("ENCRYPTION_KEY")
	if encKey == "" {
		return nil, errors.New("ENCRYPTION_KEY environment variable is required and cannot be empty")
	}
	if len(encKey) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be exactly 32 bytes long for AES-256-GCM, got %d bytes", len(encKey))
	}

	adminUser := getEnv("ADMIN_INITIAL_USERNAME", "admin")
	adminPass := os.Getenv("ADMIN_INITIAL_PASSWORD")

	dbPortStr := getEnv("PLATFORM_DB_PORT", "5432")
	dbPort, err := strconv.Atoi(dbPortStr)
	if err != nil {
		dbPort = 5432
	}

	cookieSecure := appEnv == "production"
	if secureEnv := os.Getenv("COOKIE_SECURE"); secureEnv != "" && appEnv != "production" {
		cookieSecure = secureEnv == "true" || secureEnv == "1"
	}

	cfg := &Config{
		Port:                 port,
		AppEnv:               appEnv,
		EncryptionKey:        encKey,
		AdminInitialUsername: adminUser,
		AdminInitialPassword: adminPass,
		PlatformDB: PlatformDBConfig{
			Host:     getEnv("PLATFORM_DB_HOST", "127.0.0.1"),
			Port:     dbPort,
			User:     getEnv("PLATFORM_DB_USER", "postgres"),
			Password: os.Getenv("PLATFORM_DB_PASSWORD"),
			DBName:   getEnv("PLATFORM_DB_NAME", "platform_db"),
			SSLMode:  getEnv("PLATFORM_DB_SSLMODE", "disable"),
		},
		CookieSecure: cookieSecure,
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
