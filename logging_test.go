package opcuaserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]LogLevel{
		"off": LogOff, "OFF": LogOff, " none ": LogOff,
		"error": LogError, "warn": LogWarn, "warning": LogWarn,
		"info": LogInfo, "debug": LogDebug, "trace": LogTrace,
	}
	for in, want := range cases {
		got, err := ParseLogLevel(in)
		if err != nil {
			t.Errorf("ParseLogLevel(%q) = %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseLogLevel("chatty"); err == nil {
		t.Error("an unknown level should be rejected")
	}
}

func TestLoggerHonoursLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opcua.log")
	l, closer, err := NewLogger(LogWarn, path)
	if err != nil {
		t.Fatal(err)
	}

	l.Error("an error")
	l.Warn("a warning")
	l.Info("an info line")
	l.Debug("a debug line")
	closer.Close()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{"an error", "a warning"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"an info line", "a debug line"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("log should not contain %q at warn level:\n%s", unwanted, out)
		}
	}
}

// gopcua calls the logger printf-style rather than with key/value pairs.
func TestLoggerFormatsArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opcua.log")
	l, closer, err := NewLogger(LogDebug, path)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("registered connection: %s", "127.0.0.1:1234")
	// A lone percent sign with no arguments must survive unmangled.
	l.Debug("100% done")
	closer.Close()

	b, _ := os.ReadFile(path)
	out := string(b)
	if !strings.Contains(out, "registered connection: 127.0.0.1:1234") {
		t.Errorf("arguments were not formatted:\n%s", out)
	}
	if !strings.Contains(out, "100% done") {
		t.Errorf("a message without arguments was reformatted:\n%s", out)
	}
}
