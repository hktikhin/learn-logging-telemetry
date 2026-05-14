package linkoerr

import (
	"errors"
	"fmt"
	"log/slog"

	pkgerr "github.com/pkg/errors"
)

type MultiError interface {
	error
	Unwrap() []error
}

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type errWithAttrs struct {
	error
	attrs []slog.Attr
}

func ErrorAttrs(err error) []slog.Attr {
	allAttrs := []slog.Attr{
		slog.String("message", err.Error()),
	}
	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		allAttrs = append(allAttrs, slog.String("stack_trace", fmt.Sprintf("%+v", stackErr.StackTrace())))
	}
	allAttrs = append(allAttrs, Attrs(err)...)
	return allAttrs
}

func WithAttrs(err error, args ...any) error {
	return &errWithAttrs{
		error: err,
		attrs: argsToAttr(args),
	}
}

func argsToAttr(args []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(args))
	for i := 0; i < len(args); {
		switch key := args[i].(type) {
		case slog.Attr:
			attrs = append(attrs, key)
			i++
		case string:
			if i+1 >= len(args) {
				attrs = append(attrs, slog.String("!BADKEY", key))
				i++
			} else {
				attrs = append(attrs, slog.Any(key, args[i+1]))
				i += 2
			}
		default:
			attrs = append(attrs, slog.Any("!BADKEY", args[i]))
			i++
		}
	}
	return attrs
}

func (e *errWithAttrs) Unwrap() error {
	return e.error
}

func (e *errWithAttrs) Attrs() []slog.Attr {
	return e.attrs
}

type attrError interface {
	Attrs() []slog.Attr
}

// Attrs recursively extracts all logging attributes from an error chain. In the
// case of duplicate keys, the outermost value takes precedence.
func Attrs(err error) []slog.Attr {
	var attrs []slog.Attr
	for err != nil {
		if ae, ok := err.(attrError); ok {
			attrs = append(attrs, ae.Attrs()...)
		}
		err = errors.Unwrap(err)
	}
	return attrs
}
