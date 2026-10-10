package logger

import (
    "log"
    "go.uber.org/zap"
    "go.uber.org/zap/zapcore"
)

var Log *zap.SugaredLogger

func InitLogger(debug bool) {
    var cfg zap.Config
    if debug {
        cfg = zap.NewDevelopmentConfig()
    } else {
        cfg = zap.NewProductionConfig()
    }

    cfg.EncoderConfig.TimeKey = "ts"
    cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
    cfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

    logger, err := cfg.Build(
		zap.AddCaller(),
        zap.AddCallerSkip(1),
	)
    if err != nil {
        panic(err)
    }

    Log = logger.Sugar()

    // Redirect Go standard log → Zap
    zap.RedirectStdLog(logger)
    log.SetFlags(0)
}

func Info(msg string, fields ...interface{})  { Log.Infow(msg, fields...) }
func Debug(msg string, fields ...interface{}) { Log.Debugw(msg, fields...) }
func Error(msg string, fields ...interface{}) { Log.Errorw(msg, fields...) }
func Warn(msg string, fields ...interface{}) { Log.Warnw(msg, fields...) }
func Fatal(msg string, fields ...interface{}) { Log.Fatalw(msg, fields...) }
func Panic(msg string, fields ...interface{}) { Log.Panicw(msg, fields...) }