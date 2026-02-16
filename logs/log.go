package logs

import (
	"bytes"
	"dex/pb"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
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

var logLevel = LevelInfo // 全局日志级别

// Logger 是日志接口，所有组件都应该持有这个接口的实例
type Logger interface {
	Trace(format string, v ...interface{})
	Debug(format string, v ...interface{})
	Verbose(format string, v ...interface{})
	Info(format string, v ...interface{})
	Warn(format string, v ...interface{})
	Error(format string, v ...interface{})
	GetBuffer() []*pb.LogLine
}

// GlobalRegistry 用于存储所有节点的 Logger，供 Explorer 调取
var Registry = struct {
	mu      sync.RWMutex
	loggers map[string]Logger
}{
	loggers: make(map[string]Logger),
}

// NodeLogger 是 Logger 接口的具体实现，绑定到特定节点
type NodeLogger struct {
	addr          string
	traceLogger   *log.Logger
	debugLogger   *log.Logger
	verboseLogger *log.Logger
	infoLogger    *log.Logger
	warnLogger    *log.Logger
	errorLogger   *log.Logger

	mu       sync.RWMutex
	buffer   []*pb.LogLine
	maxLines int
}

// NewNodeLogger 创建一个绑定到特定地址的 Logger
func NewNodeLogger(address string, maxLines int) *NodeLogger {
	prefix := address
	if len(address) > 7 {
		prefix = address[:7]
	}
	l := &NodeLogger{
		addr:          address,
		traceLogger:   log.New(os.Stdout, fmt.Sprintf("[TRACE]   [%s] ", prefix), log.Ldate|log.Ltime|log.Lmicroseconds),
		debugLogger:   log.New(os.Stdout, fmt.Sprintf("[DEBUG]   [%s] ", prefix), log.Ldate|log.Ltime|log.Lmicroseconds),
		verboseLogger: log.New(os.Stdout, fmt.Sprintf("[VERBOSE] [%s] ", prefix), log.Ldate|log.Ltime|log.Lmicroseconds),
		infoLogger:    log.New(os.Stdout, fmt.Sprintf("[INFO]    [%s] ", prefix), log.Ldate|log.Ltime|log.Lmicroseconds),
		warnLogger:    log.New(os.Stdout, fmt.Sprintf("[WARN]    [%s] ", prefix), log.Ldate|log.Ltime|log.Lmicroseconds),
		errorLogger:   log.New(os.Stderr, fmt.Sprintf("[ERROR]   [%s] ", prefix), log.Ldate|log.Ltime|log.Lmicroseconds),
		maxLines:      maxLines,
		buffer:        make([]*pb.LogLine, 0, maxLines),
	}

	Registry.mu.Lock()
	Registry.loggers[address] = l
	Registry.mu.Unlock()

	return l
}

func (l *NodeLogger) writeToBuffer(level, message string) {
	if l.maxLines <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// 确保消息是有效的 UTF-8，无效字符会被替换
	// 这防止了 protobuf marshal 时的 invalid UTF-8 错误
	validMessage := strings.ToValidUTF8(message, "")

	line := &pb.LogLine{
		Timestamp: time.Now().Format("15:04:05.000"),
		Level:     level,
		Message:   validMessage,
	}

	if len(l.buffer) >= l.maxLines {
		// 使用 copy 旋转而非 l.buffer[1:]，避免底层数组头部元素永远无法被 GC 回收
		copy(l.buffer, l.buffer[1:])
		l.buffer[len(l.buffer)-1] = line
	} else {
		l.buffer = append(l.buffer, line)
	}
}

func (l *NodeLogger) GetBuffer() []*pb.LogLine {
	l.mu.RLock()
	defer l.mu.RUnlock()
	res := make([]*pb.LogLine, len(l.buffer))
	copy(res, l.buffer)
	return res
}

func (l *NodeLogger) Trace(format string, v ...interface{}) {
	if logLevel <= LevelTrace {
		msg := fmt.Sprintf(format, v...)
		l.traceLogger.Print(msg)
		l.writeToBuffer("TRACE", msg)
	}
}

func (l *NodeLogger) Debug(format string, v ...interface{}) {
	if logLevel <= LevelDebug {
		msg := fmt.Sprintf(format, v...)
		l.debugLogger.Print(msg)
		l.writeToBuffer("DEBUG", msg)
	}
}

func (l *NodeLogger) Verbose(format string, v ...interface{}) {
	if logLevel <= LevelVerbose {
		msg := fmt.Sprintf(format, v...)
		l.verboseLogger.Print(msg)
		l.writeToBuffer("VERBOSE", msg)
	}
}

func (l *NodeLogger) Info(format string, v ...interface{}) {
	if logLevel <= LevelInfo {
		msg := fmt.Sprintf(format, v...)
		l.infoLogger.Print(msg)
		l.writeToBuffer("INFO", msg)
	}
}

func (l *NodeLogger) Warn(format string, v ...interface{}) {
	if logLevel <= LevelWarning {
		msg := fmt.Sprintf(format, v...)
		l.warnLogger.Print(msg)
		l.writeToBuffer("WARN", msg)
	}
}

func (l *NodeLogger) Error(format string, v ...interface{}) {
	if logLevel <= LevelError {
		msg := fmt.Sprintf(format, v...)
		l.errorLogger.Print(msg)
		l.writeToBuffer("ERROR", msg)
	}
}

// 默认和全局方法（为了兼容性，尽量少用，会尝试通过 GID 找 Logger）
var defaultLogger = NewNodeLogger("system", 2000)

// GIDToLoggerMapping 依然保留，作为 DI 不便覆盖时的兜底方案
var GIDToLoggerMapping sync.Map

func SetThreadLogger(l Logger) {
	GIDToLoggerMapping.Store(getGID(), l)
}

func getActiveLogger() Logger {
	if l, ok := GIDToLoggerMapping.Load(getGID()); ok {
		return l.(Logger)
	}
	return defaultLogger
}

// SetThreadNodeContext 保持兼容性，但建议改用依赖注入
func SetThreadNodeContext(nodeIDOrAddr string) {
	// 尝试找到对应的 Logger 并设置
	Registry.mu.RLock()
	if l, ok := Registry.loggers[normalizeNodeIdent(nodeIDOrAddr)]; ok {
		SetThreadLogger(l)
	}
	Registry.mu.RUnlock()
}

// RegisterNodeMapping 注册 ID 到地址的映射，兼容旧代码
func RegisterNodeMapping(id string, address string) {
	// ID 到地址的静态映射
	idToAddrStatic.Store(id, address)
	// 如果该地址的 Logger 已存在，确保 ID 也能映射过去（虽然地址才是主键）
	idToAddrStatic.Store(address, address)
}

var idToAddrStatic sync.Map

func normalizeNodeIdent(ident string) string {
	if addr, ok := idToAddrStatic.Load(ident); ok {
		return addr.(string)
	}
	if idx := strings.LastIndex(ident, ":"); idx != -1 {
		port := ident[idx+1:]
		if addr, ok := idToAddrStatic.Load(port); ok {
			return addr.(string)
		}
	}
	return ident
}

// gidBufPool 池化 getGID 使用的 64 字节 buffer，消除每次调用的 make([]byte, 64) 分配。
var gidBufPool = sync.Pool{New: func() any { b := make([]byte, 64); return &b }}

func getGID() uint64 {
	bp := gidBufPool.Get().(*[]byte)
	b := (*bp)[:cap(*bp)]
	b = b[:runtime.Stack(b, false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	b = b[:bytes.IndexByte(b, ' ')]
	n, _ := strconv.ParseUint(string(b), 10, 64)
	gidBufPool.Put(bp)
	return n
}

// GetLogsForNode 获取指定节点的日志，供 Explorer 调用
func GetLogsForNode(nodeIdent string) []*pb.LogLine {
	addr := normalizeNodeIdent(nodeIdent)
	Registry.mu.RLock()
	defer Registry.mu.RUnlock()
	if l, ok := Registry.loggers[addr]; ok {
		return l.GetBuffer()
	}
	return []*pb.LogLine{}
}

func GetAllLoggedNodes() []string {
	Registry.mu.RLock()
	defer Registry.mu.RUnlock()
	nodes := make([]string, 0, len(Registry.loggers))
	for k := range Registry.loggers {
		nodes = append(nodes, k)
	}
	return nodes
}

func GetLogs() []*pb.LogLine {
	return defaultLogger.GetBuffer()
}

// 包级别的全局函数，重定向到活跃 Logger
// 关键优化：先检查日志级别，避免在级别不够时白白调用 getGID() → runtime.Stack()
func Trace(format string, v ...interface{}) {
	if logLevel <= LevelTrace {
		getActiveLogger().Trace(format, v...)
	}
}
func Debug(format string, v ...interface{}) {
	if logLevel <= LevelDebug {
		getActiveLogger().Debug(format, v...)
	}
}
func Verbose(format string, v ...interface{}) {
	if logLevel <= LevelVerbose {
		getActiveLogger().Verbose(format, v...)
	}
}
func Info(format string, v ...interface{}) {
	if logLevel <= LevelInfo {
		getActiveLogger().Info(format, v...)
	}
}
func Warn(format string, v ...interface{}) {
	if logLevel <= LevelWarning {
		getActiveLogger().Warn(format, v...)
	}
}
func Error(format string, v ...interface{}) {
	if logLevel <= LevelError {
		getActiveLogger().Error(format, v...)
	}
}

// 废弃旧方法占位
func (l *NodeLogger) writeToBufferOld(level, message string) {}
