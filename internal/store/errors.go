package store

import "errors"

var (
	// ErrEmptyURL indicates a URL parameter is missing or empty
	ErrEmptyURL = errors.New("empty_url")
)
