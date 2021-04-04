package main

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// Config contains all the configuration details for the server.
// If you want to load more stuff from the environment, do it here.
// This is generally used for secrets and other more sensitive config stuff.
type Config struct {
	SentryDSN      string
	DiscordWebhook string
	DBPath         string
}

func must(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	msg := fmt.Sprintf("missing env variable: %s", key)
	panic(msg)
}

func fallback(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func optional(key string) string {
	return fallback(key, "")
}

func getConfig() *Config {
	return &Config{
		SentryDSN:      optional("SENTRY_DSN"),
		DiscordWebhook: optional("DISCORD_WEBHOOK"),
		DBPath:         must("DB_PATH"),
	}
}

var config *Config

// Called on application start.
func init() {
	rand.Seed(time.Now().UnixNano())
	_ = godotenv.Load()
	config = getConfig()
}

// GetConfig returns the singleton configuration object.
func GetConfig() *Config {
	return config
}
