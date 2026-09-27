package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port                    string
	DatabaseURL             string
	JWTSecret               string
	AccessTTL               time.Duration
	RefreshTTL              time.Duration
	CORSOrigins             string
	SeedOnBoot              bool
	AIServiceURL            string
	AITrainingToken         string
	AutomationEncryptionKey string
	AutomationServiceToken  string
}

// Load reads .env when present (absent is fine — real deployments use real env vars)
// and fails fast on anything that must not silently fall back to a default.
func Load() *Config {
	_ = godotenv.Load()

	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is required — refusing to start with a default signing key")
	}
	if len(secret) < 32 {
		log.Fatal("JWT_SECRET must be at least 32 characters")
	}

	return &Config{
		Port:                    env("PORT", "8080"),
		DatabaseURL:             databaseURL(),
		JWTSecret:               secret,
		AccessTTL:               duration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTTL:              duration("REFRESH_TOKEN_TTL", 168*time.Hour),
		CORSOrigins:             env("CORS_ORIGINS", "http://localhost:5173,http://localhost:5174"),
		SeedOnBoot:              env("SEED_ON_BOOT", "true") == "true",
		AIServiceURL:            env("AI_SERVICE_URL", "http://127.0.0.1:8000"),
		AITrainingToken:         os.Getenv("AI_TRAINING_TOKEN"),
		AutomationEncryptionKey: os.Getenv("AUTOMATION_ENCRYPTION_KEY"),
		AutomationServiceToken:  os.Getenv("AUTOMATION_SERVICE_TOKEN"),
	}
}

func databaseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=Asia/Bangkok",
		env("DB_HOST", "localhost"),
		env("DB_PORT", "5432"),
		env("DB_USER", "postgres"),
		env("DB_PASSWORD", "postgres"),
		env("DB_NAME", "mtsense"),
		env("DB_SSLMODE", "disable"),
	)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func duration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	mins, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: %s=%q is not a number of minutes, using default", key, v)
		return fallback
	}
	return time.Duration(mins) * time.Minute
}
