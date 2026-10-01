// Package logx - a unified logging package for CO, LO, EN, and edgectl
//
// Features:
// - Wrapper around zap (fast structured logger)
// - Environment-aware (development vs production)
// - Service-scoped loggers: logx.New("co")
// - Context propagation helpers: WithFields(ctx, ...), FromContext(ctx)
// - HTTP middleware to inject request IDs and attach logger to context
// - Simple CLI friendly logger adapter
// - Optional file output (rotation left to caller; example shows lumberjack usage)
//
// Usage:
//  logx.Init("dev", logx.Options{Version: "0.1.0"})
//  log := logx.New("co")
//  log.Infow("starting", "nodeID", nodeID)
//
//  ctx := logx.WithFields(context.Background(), "reqID", reqID, "deploymentID", depID)
//  l := logx.FromContext(ctx)
//  l.Debugw("processing")

package logx

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NOTE: This file is intentionally self-contained. If you want file-rotation use
// github.com/natefinch/lumberjack or other rotator and pass an io.Writer to InitOptions.

// Options for Init
type Options struct {
	// Environment: "prod" or "dev" (case-insensitive). "prod" uses JSON, "dev" uses console.
	Env string
	// Version to attach to all logs
	Version string
	// Additional writer; if nil logs go to stdout/stderr via zap's default cores
	ExtraWriters []io.Writer
	// Development caller skip - useful when wrapping
	CallerSkip int
}

var (
	baseLogger *zap.Logger
	sugar      *zap.SugaredLogger
	mu         sync.Mutex
	inited     bool
)

// Init initializes the base logger. Safe to call multiple times; subsequent calls will replace logger.
func Init(opts Options) error {
	mu.Lock()
	defer mu.Unlock()

	// default
	env := strings.ToLower(strings.TrimSpace(opts.Env))
	if env == "" {
		env = "dev"
	}

	var cfg zap.Config
	if env == "prod" {
		cfg = zap.NewProductionConfig()
		cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	} else {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.EncoderConfig.EncodeTime = zapcore.RFC3339TimeEncoder
	}

	// Adjust caller skip
	if opts.CallerSkip > 0 {
		cfg.DisableCaller = false
	}

	// Build core(s)
	encoder := zapcore.NewConsoleEncoder(cfg.EncoderConfig)
	if env == "prod" {
		encoder = zapcore.NewJSONEncoder(cfg.EncoderConfig)
	}

	// base cores: stdout (Info and below) and stderr (Error and above)
	stdout := zapcore.Lock(os.Stdout)
	stderr := zapcore.Lock(os.Stderr)

	high := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool { return lvl >= zapcore.ErrorLevel })
	low := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool { return lvl < zapcore.ErrorLevel })

	cores := []zapcore.Core{
		zapcore.NewCore(encoder, stdout, low),
		zapcore.NewCore(encoder, stderr, high),
	}

	// Add any extra writers
	for _, w := range opts.ExtraWriters {
		if w == nil {
			continue
		}
		ws := zapcore.AddSync(writerToWriteSyncer(w))
		cores = append(cores, zapcore.NewCore(encoder, ws, zapcore.DebugLevel))
	}

	core := zapcore.NewTee(cores...)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	// Attach version field so all sub-loggers inherit it
	if opts.Version != "" {
		logger = logger.With(zap.String("version", opts.Version))
	}

	// set global
	if baseLogger != nil {
		_ = baseLogger.Sync()
	}
	baseLogger = logger
	sugar = baseLogger.Sugar()
	inited = true
	return nil
}

// helper to convert io.Writer to zapcore.WriteSyncer
func writerToWriteSyncer(w io.Writer) zapcore.WriteSyncer {
	// If the writer also implements Sync, use that
	if ws, ok := w.(zapcore.WriteSyncer); ok {
		return ws
	}
	// Otherwise wrap
	return zapcore.AddSync(&nopSyncWriter{w})
}

type nopSyncWriter struct{ w io.Writer }

func (n *nopSyncWriter) Write(p []byte) (int, error) { return n.w.Write(p) }
func (n *nopSyncWriter) Sync() error                       { return nil }

// New returns a SugaredLogger with the `service` field attached.
// Example: log := logx.New("co")
func New(service string) *zap.SugaredLogger {
	ensureInit()
	if service == "" {
		return sugar
	}
	return sugar.Named(service)
}

func ensureInit() {
	mu.Lock()
	defer mu.Unlock()
	if !inited {
		// default initialization
		_ = Init(Options{Env: "dev"})
	}
}

// WithFields returns a new logger with additional structured fields.
// Useful when you have request-scoped metadata.
func WithFields(l *zap.SugaredLogger, kv ...interface{}) *zap.SugaredLogger {
	if l == nil {
		ensureInit()
		l = sugar
	}
	return l.With(kv...)
}

// Context helpers
type ctxKeyType string

const loggerKey ctxKeyType = "logx_logger"

// WithContext returns a new context with the logger attached.
func WithContext(ctx context.Context, l *zap.SugaredLogger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// WithFieldsToContext attaches fields to logger in context and returns new context
func WithFieldsToContext(ctx context.Context, kv ...interface{}) context.Context {
	l := FromContext(ctx)
	if l == nil {
		l = New("")
	}
	return WithContext(ctx, l.With(kv...))
}

// FromContext returns the logger stored in context or the base logger.
func FromContext(ctx context.Context) *zap.SugaredLogger {
	if ctx == nil {
		ensureInit()
		return sugar
	}
	if v := ctx.Value(loggerKey); v != nil {
		if l, ok := v.(*zap.SugaredLogger); ok {
			return l
		}
	}
	ensureInit()
	return sugar
}

// HTTP middleware example: inject request id and logger into context
// You can adapt this to any framework; this is plain net/http style

// ReqInfo is request metadata we attach
type ReqInfo struct {
	ReqID string
	Host  string
	URI   string
	Start time.Time
}

// HandlerFunc is simplified signature to avoid importing net/http here. If you want the
// actual middleware, copy the pattern into your http package and adapt.

// MakeRequestFields returns common fields for HTTP requests
func MakeRequestFields(reqID, host, uri string) []interface{} {
	return []interface{}{"reqID", reqID, "host", host, "uri", uri}
}

// CLI-friendly logger: for edgectl we prefer pretty output with level prefixes.
// Provide a small adapter that uses zap under the hood but formats to stdout using the
// simplest layout. Use NewCLI("cli") to get it.

// NewCLI returns a *zap.SugaredLogger that writes human-friendly logs to stdout.
func NewCLI(service string) *zap.SugaredLogger {
	ensureInit()
	// build a minimal console core that writes to stdout
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	enc := zapcore.NewConsoleEncoder(encCfg)
	core := zapcore.NewCore(enc, zapcore.AddSync(os.Stdout), zap.DebugLevel)
	logger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	if service != "" {
		logger = logger.With(zap.String("service", service))
	}
	return logger.Sugar()
}

// Convenience helpers that mirror common patterns
func Infow(msg string, kv ...interface{}) { ensureInit(); sugar.Infow(msg, kv...) }
func Debugw(msg string, kv ...interface{}) { ensureInit(); sugar.Debugw(msg, kv...) }
func Errorw(msg string, kv ...interface{}) { ensureInit(); sugar.Errorw(msg, kv...) }
func Fatalw(msg string, kv ...interface{}) { ensureInit(); sugar.Fatalw(msg, kv...) }

// Sync flushes any buffered loggers.
func Sync() error {
	mu.Lock()
	defer mu.Unlock()
	if baseLogger != nil {
		return baseLogger.Sync()
	}
	return nil
}

// Small helper: create request ID (simple). Replace with fx/rand or uuid in production.
func DefaultReqID(prefix string) string {
	if prefix == "" {
		prefix = "rid"
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// Helper: parse key/value pairs into map for pretty printing or tests
func KVToMap(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i < len(kv)-1; i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			continue
		}
		m[k] = kv[i+1]
	}
	return m
}

/*
// Simple example usage printed as a helper. Not executed.
var example = `// Example
package main

import (
	"context"
	"github.com/yourorg/yourrepo/pkg/logx"
)

func main() {
	logx.Init(logx.Options{Env: "dev", Version: "0.1.0"})
	log := logx.New("co")
	log.Infow("co started")

	ctx := logx.WithFieldsToContext(context.Background(), "reqID", logx.DefaultReqID("req"), "deploymentID", "dep-123")
	l := logx.FromContext(ctx)
	l.Debugw("processing request")

	cli := logx.NewCLI("cli")
	cli.Infow("edgectl running", "command", "deploy")
}
*/
