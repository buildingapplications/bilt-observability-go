package obs

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func newCapturedLogger(w zapcore.WriteSyncer) *zap.SugaredLogger {
	enc := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		MessageKey:     "msg",
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	})
	return zap.New(zapcore.NewCore(enc, w, zap.InfoLevel)).Sugar()
}
