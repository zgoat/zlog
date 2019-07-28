// Package zlog is a logging library.
package zlog // import "zgo.at/zlog"

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/teamwork/utils/errorutil"
)

// ConfigT is the configuration struct.
type ConfigT struct {
	// Time/date format as accepted by time.Format().
	//
	// The default is to just print the time, which works well in development.
	// For production you probably want to use time.RFC3339 or some such.
	//
	// This is used in the standard format() function, not not elsewhere.
	FmtTime string

	// Format function used by the default stdout/stderr output. This takes a
	// Log entry and formats it for output.
	Format func(Log) string

	// Outputs for a Log entry.
	//
	// The default is to print to stderr for errors, and stdout for everything
	// else. Generally you want to keep this as a backup and add other
	// additional outputs, for example:
	//
	//    zlog.Config.Outputs = append(zlog.Config.Outputs, func(l Log) {
	//        if l.Level != LevelErr { // Only process errors.
	//            return
	//        }
	//
	//        // .. send to external error notification service ..
	//    })
	//
	//    zlog.Config.Outputs = append(zlog.Config.Outputs, func(l Log) {
	//        if l.Level == LevelErr { // Only process non-errors.
	//            return
	//        }
	//
	//        // .. send to external logging service ..
	//    })
	Outputs []func(Log)

	// Filter stack traces, only used if the error is a github.com/pkg/errors
	// with a stack trace.
	// Useful to filter out HTTP middleware and other useless stuff.
	StackFilter *errorutil.Patterns

	// Global debug modules; always print debug information for these.
	Debug []string
}

func (c ConfigT) RunOutputs(l Log) {
	for _, o := range c.Outputs {
		o(l)
	}
}

// Config for this package.
var Config ConfigT

func init() {
	Config = ConfigT{
		FmtTime: "15:04:05 ",
		Format:  format,
		Outputs: []func(Log){output},
	}
}

// Debug levels.
const (
	LevelInfo  = 0
	LevelErr   = 1
	LevelDbg   = 2
	LevelTrace = 3
)

var now = time.Now

type (
	// Log module.
	Log struct {
		Ctx          context.Context
		Msg          string   // Log message; set with Print(), Debug(), etc.
		Err          error    // Original error, set with Error().
		Level        int      // 0: print, 1: err, 2: debug, 3: trace
		Modules      []string // Modules added to the logger.
		Data         F        // Fields added to the logger.
		DebugModules []string // List of modules to debug.
		Traces       []string // Traces added to the logger.

		since time.Time
	}

	// F are log fields.
	F map[string]interface{}
)

func Module(m string) Log   { return Log{Modules: []string{m}, since: time.Now()} }
func Fields(f F) Log        { return Log{Data: f} }
func Debug(m ...string) Log { return Log{DebugModules: m} }

func Print(v ...interface{})            { Log{}.Print(v...) }
func Printf(f string, v ...interface{}) { Log{}.Printf(f, v...) }
func Error(err error)                   { Log{}.Error(err) }
func Errorf(f string, v ...interface{}) { Log{}.Errorf(f, v...) }

func (l Log) ResetTrace()                 { l.Traces = []string{} }
func (l Log) Context(ctx context.Context) { l.Ctx = ctx }

func (l Log) Module(m string) Log {
	l.Modules = append(l.Modules, m)
	l.since = time.Now()
	return l
}

func (l Log) Fields(f F) Log {
	l.Data = f
	return l
}

func (l Log) Print(v ...interface{}) {
	l.Msg = fmt.Sprint(v...)
	l.Level = LevelInfo
	Config.RunOutputs(l)
}

func (l Log) Printf(f string, v ...interface{}) {
	l.Msg = fmt.Sprintf(f, v...)
	l.Level = LevelInfo
	Config.RunOutputs(l)
}

func (l Log) Error(err error) {
	l.Err = err
	l.Level = LevelErr
	Config.RunOutputs(l)
}

func (l Log) Errorf(f string, v ...interface{}) {
	l.Err = fmt.Errorf(f, v...)
	l.Level = LevelErr
	Config.RunOutputs(l)
}

func (l Log) Debug(v ...interface{}) {
	if !l.hasDebug() {
		return
	}
	l.Msg = fmt.Sprint(v...)
	l.Level = LevelDbg
	Config.RunOutputs(l)
}

func (l Log) Debugf(f string, v ...interface{}) {
	if !l.hasDebug() {
		return
	}
	l.Msg = fmt.Sprintf(f, v...)
	l.Level = LevelDbg
	Config.RunOutputs(l)
}

func (l Log) Trace(v ...interface{}) Log {
	l.Msg = fmt.Sprint(v...)
	l.Level = LevelTrace
	if l.hasDebug() {
		Config.RunOutputs(l)
		return l
	}

	l.Traces = append(l.Traces, Config.Format(l))
	return l
}

func (l Log) Tracef(f string, v ...interface{}) Log {
	l.Msg = fmt.Sprintf(f, v...)
	l.Level = LevelTrace
	if l.hasDebug() {
		Config.RunOutputs(l)
		return l
	}

	l.Traces = append(l.Traces, Config.Format(l))
	return l
}

// Request adds information from a HTTP request as fields.
func Request(r *http.Request) Log {
	if r == nil {
		panic("zlog.Request: *http.Request is nil")
	}

	return Log{}.Request(r)
}

// Request adds information from a HTTP request as fields.
func (l Log) Request(r *http.Request) Log {
	if r == nil {
		panic("zlog.Request: *http.Request is nil")
	}

	return l.Fields(F{
		"http_method": r.Method,
		"http_url":    r.URL.String(),
		"http_form":   r.Form.Encode(),
	})
}

// TODO: we could store as map[string]struct{} internally so it's a bit faster.
// Not sure if that's actually worth it?
func (l Log) hasDebug() bool {
	for _, m := range l.Modules {
		for _, d := range Config.Debug {
			if d == m {
				return true
			}
		}

		for _, d := range l.DebugModules {
			if d == m {
				return true
			}
		}
	}
	return false
}

var stderr io.Writer = os.Stderr

// Since prints the duration since the last Since() or Module() call with the
// given message.
//
// It's mainly intended for quick printf-style performance debugging and will
// only work in debug mode. It will always output to stderr.
func (l Log) Since(msg string) Log {
	if !l.hasDebug() {
		return l
	}

	n := time.Now()
	if l.since.IsZero() {
		l.since = n
	}

	fmt.Fprintf(stderr, "  %s %5dms  %s\n",
		strings.Join(l.Modules, ":"),
		time.Since(l.since).Nanoseconds()/1000000, msg)
	l.since = n
	return l
}

// Recover from a panic.
//
//   go func() {
//       defer zlog.Recover()
//       // ... do work...
//   }()
//
// Any panics will be recover()'d and reported with Error().
//
// The first callback will be called *before* the Error() call, and can be used
// to modify the Log instance, for example to add fields:
//
//   defer zlog.Recover(func(l Log) Log {
//       return l.Fields(zlog.F{"id": id})
//   })
//
// Any other callbacks will be called *after* the Error() call. Modifying the
// Log instance has no real use.
func Recover(cb ...func(Log) Log) {
	r := recover()
	if r == nil {
		return
	}

	err, ok := r.(error)
	if !ok {
		err = fmt.Errorf("%v", r)
	}

	l := Module("panic")
	if len(cb) > 0 {
		l = cb[0](l)
	}

	l.Error(err)

	if len(cb) > 1 {
		for i := range cb[1:] {
			l = cb[i](l)
		}
	}
}
