package storer

import (
	"context"
	"errors"
	"os"
	"strings"
)

// New creates a storer based on the `uri` string.
func New(ctx context.Context, uri, cert string) (Storer, error) {
	switch {
	case uri == "", strings.EqualFold(uri, "stdout"):
		return LogStorer{w: os.Stdout}, nil
	case strings.EqualFold(uri, "stderr"):
		return LogStorer{w: os.Stderr}, nil
	case strings.HasPrefix(uri, "mysql:"):
		return NewMySQLStorer(ctx, uri, cert)
	default:
		return nil, errors.New("unsupported storer uri format")
	}
}
