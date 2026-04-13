/*
Copyright 2025 The OpenSaola Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package logger provides logging utilities.
package logger

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"gopkg.in/natefinch/lumberjack.v2"
)

/*
logger.go handles logging operations.
*/

var Log *Loggers

type Loggers struct {
	Zlog zerolog.Logger
}

// Initialize initializes the logger.
func Initialize(lv zerolog.Level) {
	Log = new(Loggers)

	var writers []io.Writer
	if fw := fileWriter(); fw != nil {
		writers = append(writers, fw)
	}
	writers = append(writers, stdWriter())

	Log.Zlog = zerolog.New(zerolog.MultiLevelWriter(writers...)).
		With().
		Timestamp().
		CallerWithSkipFrameCount(3).
		Logger().Level(zerolog.Level(lv))
	Log.Zlog.Info().Msg("zerolog initialized successfully")
}

func fileWriter() io.Writer {
	fp := viper.GetString("log.file_path")
	if fp == "" {
		return nil
	}

	dir := filepath.Dir(fp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] failed to create log directory %s: %v, file logging disabled\n", dir, err)
		return nil
	}

	f, err := os.OpenFile(fp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] failed to open log file %s: %v, file logging disabled\n", fp, err)
		return nil
	}
	f.Close()

	return rotateWriter(fp)
}

func stdWriter() io.Writer {
	return zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.TimeFormat = time.DateTime
		// w.FormatLevel = func(i interface{}) string {
		// 	return strings.ToUpper(fmt.Sprintf("[%s]", i))
		// }
		// w.FormatCaller = func(i interface{}) string {
		// 	return fmt.Sprintf("%s", i)
		// }
		w.Out = os.Stdout
	})
}

func rotateWriter(filename string) io.Writer {
	return &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    viper.GetInt("log.size"), // megabytes
		MaxBackups: viper.GetInt("log.count"),
		LocalTime:  true,
	}
}

func (l *Loggers) Print(i ...interface{}) {
	l.Zlog.Info().Msg(fmt.Sprint(i...))
}

func (l *Loggers) Printf(format string, args ...interface{}) {
	l.Zlog.Info().Msgf(format, args...)
}

func (l *Loggers) Printj(j map[string]interface{}) {
	b, _ := json.Marshal(j)
	l.Zlog.Info().Msg(string(b))
}

func (l *Loggers) Debug(i ...interface{}) {
	l.Zlog.Debug().Msg(fmt.Sprint(i...))
}

func (l *Loggers) Debugf(format string, args ...interface{}) {
	l.Zlog.Debug().Msgf(format, args...)
}

func (l *Loggers) Debugj(j map[string]interface{}) {
	if l.Zlog.GetLevel() <= zerolog.DebugLevel {
		b, _ := json.Marshal(j)
		l.Zlog.Debug().Msg(string(b))
	}
}

func (l *Loggers) Info(i ...interface{}) {
	l.Zlog.Info().Msg(fmt.Sprint(i...))
}

func (l *Loggers) Infof(format string, args ...interface{}) {
	l.Zlog.Info().Msgf(format, args...)
}

func (l *Loggers) Infoj(j map[string]interface{}) {
	if l.Zlog.GetLevel() <= zerolog.InfoLevel {
		b, _ := json.Marshal(j)
		l.Zlog.Info().Msg(string(b))
	}
}

func (l *Loggers) Warn(i ...interface{}) {
	l.Zlog.Warn().Msg(fmt.Sprint(i...))
}

func (l *Loggers) Warnf(format string, args ...interface{}) {
	l.Zlog.Warn().Msgf(format, args...)
}

func (l *Loggers) Warnj(j map[string]interface{}) {
	if l.Zlog.GetLevel() <= zerolog.WarnLevel {
		b, _ := json.Marshal(j)
		l.Zlog.Warn().Msg(string(b))
	}
}

func (l *Loggers) Error(i ...interface{}) {
	l.Zlog.Error().Msg(fmt.Sprint(i...))
}

func (l *Loggers) Errorf(format string, args ...interface{}) {
	l.Zlog.Error().Msgf(format, args...)
}

func (l *Loggers) Errorj(j map[string]interface{}) {
	if l.Zlog.GetLevel() <= zerolog.ErrorLevel {
		b, _ := json.Marshal(j)
		l.Zlog.Error().Stack().Msg(string(b))
	}
}

func (l *Loggers) Fatal(i ...interface{}) {
	l.Zlog.Fatal().Msg(fmt.Sprint(i...))
}

func (l *Loggers) Fatalj(j map[string]interface{}) {
	if l.Zlog.GetLevel() <= zerolog.FatalLevel {
		b, _ := json.Marshal(j)
		l.Zlog.Fatal().Msg(string(b))
	}
}

func (l *Loggers) Fatalf(format string, args ...interface{}) {
	l.Zlog.Fatal().Msgf(format, args...)
}

func (l *Loggers) Panic(i ...interface{}) {
	l.Zlog.Panic().Msg(fmt.Sprint(i...))
}

func (l *Loggers) Panicj(j map[string]interface{}) {
	if l.Zlog.GetLevel() <= zerolog.PanicLevel {
		b, _ := json.Marshal(j)
		l.Zlog.Panic().Msg(string(b))
	}
}

func (l *Loggers) Panicf(format string, args ...interface{}) {
	l.Zlog.Panic().Msgf(format, args...)
}
