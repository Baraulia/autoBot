package logger

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"golang.org/x/term"
)

type ctxKey int

const (
	loggerKey ctxKey = iota
)

var (
	once        sync.Once
	logger      *logrus.Logger
	globalEntry *logrus.Entry
)

// NewLogger создаем глобальный логгер для проекта
func NewLogger() *logrus.Logger {
	once.Do(func() {
		level := logrus.InfoLevel
		deb, ok := os.LookupEnv("DEB")
		if ok {
			level = logrus.DebugLevel
		}

		if strings.EqualFold(deb, "trace") {
			level = logrus.TraceLevel
		}

		format := "2006-01-02 15:04:05.000"
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			format = "15:04:05.000"
		}

		logger = &logrus.Logger{
			Out:   os.Stderr,
			Level: level,
			Formatter: &logrus.TextFormatter{
				// DisableColors:   false,
				TimestampFormat: format,
				FullTimestamp:   true,
			},
		}

		logger.SetReportCaller(true)

		globalEntry = logrus.NewEntry(logger)
	})

	return logger
}

// PutLoggerIntoContext помещаем логгер в контекст
func PutLoggerIntoContext(ctx context.Context, entry *logrus.Entry) context.Context {
	if entry == nil {
		entry = globalEntry
	}

	return context.WithValue(ctx, loggerKey, entry)
}

// GetLoggerFromContext достаем логгер из контекста, если его нет - отдаем глобальный
func GetLoggerFromContext(ctx context.Context) *logrus.Entry {
	if entry, ok := ctx.Value(loggerKey).(*logrus.Entry); ok {
		return entry
	}

	return globalEntry
}
