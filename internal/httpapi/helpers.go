package httpapi

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

func contextWithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

func urlParse(raw string) (*url.URL, error) { return url.Parse(raw) }

func parsePositiveID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		if err == nil {
			err = strconv.ErrSyntax
		}
		return 0, err
	}
	return id, nil
}
