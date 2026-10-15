package log

import (
	"fmt"
	"os"
	"time"
)

func Debug(module string, format string, args ...interface{}) {
	write("DEBUG", module, format, args...)
}

func Info(module string, format string, args ...interface{}) {
	write("INFO", module, format, args...)
}

func Warn(module string, format string, args ...interface{}) {
	write("WARN", module, format, args...)
}

func Error(module string, format string, args ...interface{}) {
	write("ERROR", module, format, args...)
}

func write(level, module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	now := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(os.Stderr, "[%s] [%s] %s: %s\n", now, level, module, msg)
}
