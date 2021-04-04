package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"reflect"

	"github.com/getsentry/sentry-go"
)

// Logger can write logs to somewhere.
type Logger interface {
	Logf(format string, args ...interface{})
	Errorf(err error, format string, args ...interface{})
}

// Fatalf can write logs to a logger, then exit the program with non-ok status code.
func Fatalf(logger Logger, err error, format string, args ...interface{}) {
	logger.Errorf(err, format, args...)
	os.Exit(1)
}

type multiLogger struct {
	loggers []Logger
}

func (ml *multiLogger) Logf(format string, args ...interface{}) {
	for _, l := range ml.loggers {
		l.Logf(format, args...)
	}
}

func (ml *multiLogger) Errorf(err error, format string, args ...interface{}) {
	for _, l := range ml.loggers {
		l.Errorf(err, format, args...)
	}
}

// NewMultiLogger takes a set of loggers and returns a logger which writes to all of them.
func NewMultiLogger(loggers ...Logger) Logger {
	return &multiLogger{loggers: loggers}
}

type baseLogger struct{}

func (bl *baseLogger) Logf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[I] %s", msg)
}

func (bl *baseLogger) Errorf(err error, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if err != nil {
		msg = fmt.Sprintf("%s: %v", msg, err)
	}
	log.Printf("[E] %s", msg)
}

func NewBaseLogger() Logger {
	return &baseLogger{}
}

type discordLogger struct {
	whurl  string
	backup Logger // Catching errors.
}

func (dl *discordLogger) logMessage(msg string) {
	params := url.Values{}
	params.Set("content", msg)
	resp, err := http.PostForm(dl.whurl, params)
	if err != nil {
		dl.backup.Errorf(err, "error posting to discord webhook")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		dl.backup.Errorf(nil, "discord webhook returned non-ok status %d: %s", resp.StatusCode, resp.Status)
	}
}

func (dl *discordLogger) Logf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	dl.logMessage(msg)
}

func (dl *discordLogger) Errorf(err error, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if err != nil {
		msg = fmt.Sprintf("%s: %v", msg, err)
	}
	msg = "[Unexpected] " + msg
	dl.logMessage(msg)
}

// NewDiscordLogger constructs a logger which writes messages to a specific Discord
// webhook endpoint. Note that Discord has a pretty aggressive rate limit so be careful.
func NewDiscordLogger(whurl string, backup Logger) Logger {
	return &discordLogger{whurl, backup}
}

// SentryLoggerOptions can be used to configure the Sentry logger.
type SentryLoggerOptions struct {
	// If this is set to true, Logf will be a no-op.
	OnlyErrors bool
}

type sentryLogger struct {
	client *sentry.Client
	opts   *SentryLoggerOptions
}

func (sl *sentryLogger) Logf(format string, args ...interface{}) {
	// Don't log messages if OnlyErrors flag is set.
	if sl.opts.OnlyErrors {
		return
	}
	// TODO(morgangallant): Can I pass scope/hint as nil?
	sl.client.CaptureMessage(fmt.Sprintf(format, args...), nil, nil)
}

// The caller should guarentee that err is non-nil.
// Stolen from Sentry SDK.
func extractExceptionFromError(err error) []sentry.Exception {
	ret := []sentry.Exception{}
	for i := 0; i < 10 && err != nil; i++ {
		ret = append(ret, sentry.Exception{
			Value:      err.Error(),
			Type:       reflect.TypeOf(err).String(),
			Stacktrace: sentry.ExtractStacktrace(err),
		})
		switch previous := err.(type) {
		case interface{ Unwrap() error }:
			err = previous.Unwrap()
		case interface{ Cause() error }:
			err = previous.Cause()
		default:
			err = nil
		}
	}

	// Add a trace of the current stack to the most recent error in a chain if
	// it doesn't have a stack trace yet.
	// We only add to the most recent error to avoid duplication and because the
	// current stack is most likely unrelated to errors deeper in the chain.
	if ret[0].Stacktrace == nil {
		ret[0].Stacktrace = sentry.NewStacktrace()
	}

	// ret should be sorted such that the most recent error is last.
	for i := len(ret)/2 - 1; i >= 0; i-- {
		opp := len(ret) - 1 - i
		ret[i], ret[opp] = ret[opp], ret[i]
	}

	return ret
}

func (sl *sentryLogger) Errorf(err error, format string, args ...interface{}) {
	event := &sentry.Event{
		Message: fmt.Sprintf(format, args...),
		Level:   sentry.LevelError,
	}
	if err != nil {
		event.Exception = extractExceptionFromError(err)
	}
	// TODO(morgangallant): Can I pass scope/hint as nil?
	sl.client.CaptureEvent(event, nil, nil)
}

// NewSentryLogger creates a new logger which writes messages to Sentry.
func NewSentryLogger(dsn string, opts *SentryLoggerOptions) Logger {
	sopts := sentry.ClientOptions{
		Dsn: dsn,
		// TODO(morgangallant): More options here?
	}
	// Best effort approach.
	if hn, err := os.Hostname(); err != nil {
		sopts.ServerName = hn
	}
	client, _ := sentry.NewClient(sopts)
	if opts == nil {
		opts = &SentryLoggerOptions{}
	}
	return &sentryLogger{client, opts}
}

// TODO(morgangallant): file logger w/ json format
