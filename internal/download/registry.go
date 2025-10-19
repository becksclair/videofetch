package download

import (
	"fmt"
	"sync"
	"time"
)

// ItemRegistry provides thread-safe storage and manipulation of download items.
// It acts as a pure state container without any download logic or external dependencies.
type ItemRegistry struct {
	mu        sync.RWMutex
	downloads map[string]*Item
}

// NewItemRegistry creates a new ItemRegistry with the specified initial capacity.
func NewItemRegistry(capacity int) *ItemRegistry {
	if capacity <= 0 {
		capacity = 128
	}
	return &ItemRegistry{
		downloads: make(map[string]*Item, capacity),
	}
}

// Create adds a new item to the registry and returns it.
// Returns an error if an item with the given ID already exists.
func (r *ItemRegistry) Create(id, url string) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.downloads[id]; exists {
		return nil, fmt.Errorf("item with id %s already exists", id)
	}

	it := &Item{
		ID:        id,
		URL:       url,
		Progress:  0,
		State:     StateQueued,
		startedAt: time.Now(),
		updatedAt: time.Now(),
	}
	r.downloads[id] = it
	return it, nil
}

// Get retrieves a single item by ID.
// Returns nil if the item doesn't exist.
func (r *ItemRegistry) Get(id string) *Item {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if it, ok := r.downloads[id]; ok {
		// Return a copy to prevent external modification
		cp := *it
		return &cp
	}
	return nil
}

// Update atomically updates an item using the provided function.
// Returns an error if the item doesn't exist.
func (r *ItemRegistry) Update(id string, fn func(*Item)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	it, ok := r.downloads[id]
	if !ok {
		return fmt.Errorf("item with id %s not found", id)
	}

	fn(it)
	it.updatedAt = time.Now()
	return nil
}

// Snapshot returns a copy of all items in the registry.
// If id is non-empty, returns at most that single item.
func (r *ItemRegistry) Snapshot(id string) []*Item {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if id != "" {
		if it, ok := r.downloads[id]; ok {
			cp := *it
			return []*Item{&cp}
		}
		return []*Item{}
	}

	out := make([]*Item, 0, len(r.downloads))
	for _, it := range r.downloads {
		cp := *it
		out = append(out, &cp)
	}
	return out
}

// Attach sets the database ID for an item.
// Returns an error if the item doesn't exist.
func (r *ItemRegistry) Attach(id string, dbID int64) error {
	return r.Update(id, func(it *Item) {
		it.DBID = dbID
	})
}

// SetMeta updates the metadata fields for an item.
// Returns an error if the item doesn't exist.
func (r *ItemRegistry) SetMeta(id, title string, duration int64, thumbnail string) error {
	return r.Update(id, func(it *Item) {
		if title != "" {
			it.Title = title
		}
		if duration > 0 {
			it.Duration = duration
		}
		if thumbnail != "" {
			it.ThumbnailURL = thumbnail
		}
	})
}

// SetProgress updates the progress for an item.
// Only increases the progress value (never decreases).
// Returns the previous and new progress values.
func (r *ItemRegistry) SetProgress(id string, progress float64) (float64, float64, error) {
	var prev, new float64
	err := r.Update(id, func(it *Item) {
		prev = it.Progress
		// Only increase progress (yt-dlp prints for multiple phases)
		if progress > it.Progress {
			it.Progress = progress
			new = progress
		} else {
			new = prev
		}
	})
	return prev, new, err
}

// SetState updates the state and optional error message for an item.
// Returns an error if the item doesn't exist.
func (r *ItemRegistry) SetState(id string, state State, errMsg string) error {
	return r.Update(id, func(it *Item) {
		it.State = state
		it.Error = errMsg
	})
}

// SetFilename updates the filename for an item.
// Returns an error if the item doesn't exist.
func (r *ItemRegistry) SetFilename(id, filename string) error {
	return r.Update(id, func(it *Item) {
		it.Filename = filename
	})
}

// Delete removes an item from the registry.
// Returns true if the item existed and was deleted.
func (r *ItemRegistry) Delete(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.downloads[id]; ok {
		delete(r.downloads, id)
		return true
	}
	return false
}

// Size returns the number of items in the registry.
func (r *ItemRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.downloads)
}

// GetWithDBID retrieves the item with matching DBID if present.
func (r *ItemRegistry) GetWithDBID(dbID int64) *Item {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, it := range r.downloads {
		if it.DBID == dbID {
			cp := *it
			return &cp
		}
	}
	return nil
}
