package logs

import (
	"log"
	"os"
)

// 定义日志级别常量（数值越大，级别越高）
const (
	LevelTrace   = iota // 0（最低，最详细）
	LevelDebug          // 1
	LevelVerbose        // 2（新增级别）
	LevelInfo           // 3
	LevelWarning        // 4
	LevelError          // 5（最高，最严重）
)

var logLevel = LevelInfo // 全局日志级别（示例设置为 LevelVerbose）

// 全局 Logger 实例
var logger *Logger
var MyAddress = "0x0000000"
var IsCurrentLeader = ""

// Logger 结构体
type Logger struct {
	traceLogger   *log.Logger
	debugLogger   *log.Logger
	verboseLogger *log.Logger
	infoLogger    *log.Logger
	warnLogger    *log.Logger
	errorLogger   *log.Logger
}

// 初始化全局 Logger 实例
func init() {
	logger = &Logger{
		traceLogger:   log.New(os.Stdout, "[TRACE]   ", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile),
		debugLogger:   log.New(os.Stdout, "[DEBUG]   ", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile),
		verboseLogger: log.New(os.Stdout, "[VERBOSE] ", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile),
		infoLogger:    log.New(os.Stdout, "[INFO]    ", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile),
		warnLogger:    log.New(os.Stdout, "[WARN]    ", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile),
		errorLogger:   log.New(os.Stderr, "[ERROR]   ", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile),
	}
}

// 包级别的日志方法
func Trace(format string, v ...interface{}) {
	if logLevel <= LevelTrace {
		logger.traceLogger.Printf(IsCurrentLeader+" "+MyAddress[:7]+format, v...)
	}
}

func Debug(format string, v ...interface{}) {
	if logLevel <= LevelDebug {
		logger.debugLogger.Printf(IsCurrentLeader+" "+MyAddress[:7]+" "+format, v...)
	}
}

func Verbose(format string, v ...interface{}) {
	if logLevel <= LevelVerbose {
		logger.verboseLogger.Printf(IsCurrentLeader+" "+MyAddress[:7]+" "+format, v...)
	}
}

func Info(format string, v ...interface{}) {
	if logLevel <= LevelInfo {
		logger.infoLogger.Printf(IsCurrentLeader+" "+MyAddress[:7]+" "+format, v...)
	}
}

func Warn(format string, v ...interface{}) {
	if logLevel <= LevelWarning {
		logger.warnLogger.Printf(IsCurrentLeader+" "+MyAddress[:7]+" "+format, v...)
	}
}

func Error(format string, v ...interface{}) {
	if logLevel <= LevelError {
		logger.errorLogger.Printf(IsCurrentLeader+" "+MyAddress[:7]+" "+format, v...)
	}
}
