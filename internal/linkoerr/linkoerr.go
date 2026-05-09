package linkoerr

import "log/slog"

type errWithAttrs struct {
	err   error
	attrs []slog.Attr
}

func (e errWithAttrs) Error() string {
	return e.err.Error()
}

func (e errWithAttrs) Unwrap() error {
	return e.err
}

func (e errWithAttrs) Attrs() []slog.Attr {
	attrs := make([]slog.Attr, len(e.attrs))
	copy(attrs, e.attrs)
	return attrs
}

func WithAttrs(err error, args ...any) error {
	if err == nil {
		return nil
	}
	return errWithAttrs{
		err:   err,
		attrs: attrsFromArgs(args...),
	}
}

func Attrs(err error) []slog.Attr {
	var attrs []slog.Attr
	for err != nil {
		if attrErr, ok := err.(interface{ Attrs() []slog.Attr }); ok {
			attrs = append(attrs, attrErr.Attrs()...)
		}
		unwrapErr, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapErr.Unwrap()
	}
	return attrs
}

func attrsFromArgs(args ...any) []slog.Attr {
	var attrs []slog.Attr
	for len(args) > 0 {
		switch first := args[0].(type) {
		case slog.Attr:
			attrs = append(attrs, first)
			args = args[1:]
		case string:
			if len(args) == 1 {
				attrs = append(attrs, slog.String("!BADKEY", first))
				args = args[1:]
				continue
			}
			attrs = append(attrs, slog.Any(first, args[1]))
			args = args[2:]
		default:
			attrs = append(attrs, slog.Any("!BADKEY", first))
			args = args[1:]
		}
	}
	return attrs
}
