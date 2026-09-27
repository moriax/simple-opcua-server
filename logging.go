package opcuaserver

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua/debug"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

// LogLevel selects how much the server reports about what clients are doing.
type LogLevel int

const (
	LogOff LogLevel = iota
	LogError
	LogWarn
	LogInfo
	LogDebug
	LogTrace
)

var logLevelNames = map[string]LogLevel{
	"off": LogOff, "none": LogOff, "silent": LogOff,
	"error": LogError,
	"warn":  LogWarn, "warning": LogWarn,
	"info":  LogInfo,
	"debug": LogDebug,
	"trace": LogTrace,
}

// ParseLogLevel validates a -log flag value.
func ParseLogLevel(s string) (LogLevel, error) {
	if l, ok := logLevelNames[strings.ToLower(strings.TrimSpace(s))]; ok {
		return l, nil
	}
	return 0, fmt.Errorf("unknown log level %q, want off, error, warn, info, debug or trace", s)
}

func (l LogLevel) String() string {
	switch l {
	case LogOff:
		return "off"
	case LogError:
		return "error"
	case LogWarn:
		return "warn"
	case LogInfo:
		return "info"
	case LogDebug:
		return "debug"
	default:
		return "trace"
	}
}

// Logger satisfies gopcua's server.Logger. The library calls it printf-style,
// so the arguments are formatted rather than treated as key/value pairs.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	level LogLevel
}

// NewLogger writes to stderr, and additionally to logFile when it is not empty.
// The returned closer flushes and closes that file.
func NewLogger(level LogLevel, logFile string) (*Logger, io.Closer, error) {
	var out io.Writer = os.Stderr
	var closer io.Closer

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot open the log file: %w", err)
		}
		out = io.MultiWriter(os.Stderr, f)
		closer = f
	}

	l := &Logger{out: out, level: level}

	// gopcua writes some of its output through the standard logger and the
	// rest through its own debug package. Point both at the same place so one
	// log file holds the whole picture.
	log.SetFlags(0)
	log.SetOutput(prefixWriter{l: l, level: LogInfo, prefix: "gopcua"})
	debug.Logger = log.New(prefixWriter{l: l, level: LogDebug, prefix: "wire"}, "", 0)
	debug.Enable = level >= LogDebug
	if level >= LogTrace {
		// "codec" makes the encoder dump every message it decodes.
		debug.Flags = "debug codec"
	}

	return l, closer, nil
}

func (l *Logger) logf(level LogLevel, tag, msg string, args ...any) {
	if l == nil || l.level < level {
		return
	}
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	msg = strings.TrimRight(msg, "\n")

	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.out, "%s %-5s %s\n", time.Now().Format("15:04:05.000"), tag, msg)
}

func (l *Logger) Error(msg string, args ...any) { l.logf(LogError, "ERROR", msg, args...) }
func (l *Logger) Warn(msg string, args ...any)  { l.logf(LogWarn, "WARN", msg, args...) }
func (l *Logger) Info(msg string, args ...any)  { l.logf(LogInfo, "INFO", msg, args...) }
func (l *Logger) Debug(msg string, args ...any) { l.logf(LogDebug, "DEBUG", msg, args...) }

// Level reports the configured verbosity.
func (l *Logger) Level() LogLevel { return l.level }

// prefixWriter adapts a line-oriented writer, such as the standard logger, onto
// the Logger.
type prefixWriter struct {
	l      *Logger
	level  LogLevel
	prefix string
}

func (w prefixWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			w.l.logf(w.level, strings.ToUpper(w.prefix), "%s", line)
		}
	}
	return len(p), nil
}

// LogEndpoints writes out every endpoint the server advertises. This is the
// first thing to check when a client reports a rejected security policy,
// because such a client is comparing its own requirements against this list.
func (l *Logger) LogEndpoints(srv *server.Server) {
	if l == nil || l.level < LogInfo {
		return
	}
	eps := srv.Endpoints()
	l.Info("advertising %d endpoint(s)", len(eps))
	for i, ep := range eps {
		l.Info("endpoint[%d] url=%s", i, ep.EndpointURL)
		l.Info("endpoint[%d]   securityMode=%s policy=%s securityLevel=%d",
			i, SecurityModeName(ep.SecurityMode), PolicyName(ep.SecurityPolicyURI), ep.SecurityLevel)
		l.Info("endpoint[%d]   serverCertificate=%d bytes transport=%s",
			i, len(ep.ServerCertificate), ep.TransportProfileURI)
		if ep.Server != nil {
			l.Info("endpoint[%d]   applicationUri=%q productUri=%q discoveryUrls=%v",
				i, ep.Server.ApplicationURI, ep.Server.ProductURI, ep.Server.DiscoveryURLs)
		}
		for _, t := range ep.UserIdentityTokens {
			l.Info("endpoint[%d]   userToken policyId=%q type=%s policy=%q",
				i, t.PolicyID, userTokenName(t.TokenType), t.SecurityPolicyURI)
		}
	}
}

func userTokenName(t ua.UserTokenType) string {
	return strings.TrimPrefix(t.String(), "UserTokenType")
}
