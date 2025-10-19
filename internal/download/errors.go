package download

import "errors"

var (
	// ErrQueueFull indicates the download queue is at capacity
	ErrQueueFull = errors.New("queue_full")

	// ErrShuttingDown indicates the manager is no longer accepting new downloads
	ErrShuttingDown = errors.New("shutting_down")

	// ErrNoMediaInfo indicates metadata extraction produced no results
	ErrNoMediaInfo = errors.New("no_media_info")
)
