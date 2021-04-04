package main

import (
	"github.com/pkg/errors"
)

func configureLogger(cfg *Config) Logger {
	pieces := []Logger{NewBaseLogger()}
	if cfg.SentryDSN != "" {
		opts := &SentryLoggerOptions{
			OnlyErrors: true,
		}
		pieces = append(pieces, NewSentryLogger(cfg.SentryDSN, opts))
	}
	if cfg.DiscordWebhook != "" {
		pieces = append(pieces, NewDiscordLogger(cfg.DiscordWebhook, NewBaseLogger()))
	}
	return NewMultiLogger(pieces...)
}

func main() {
	cfg := GetConfig()
	logger := configureLogger(cfg)
	if err := run(logger, cfg); err != nil {
		Fatalf(logger, err, "error in run()")
	}
}

func run(logger Logger, cfg *Config) error {
	_, err := LoadDBPool(cfg)
	if err != nil {
		return errors.Wrap(err, "failed to load database connection pool")
	}
	logger.Logf("Hello World!")
	return nil
}
