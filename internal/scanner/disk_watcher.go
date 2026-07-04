package scanner

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DiskWatcher tracks in-flight disk usage and throttles downloads when approaching budget.
// Download workers call Acquire before downloading and Update after the download completes
// with the actual file size. Scan workers call Release after cleaning up a temp file.
// This creates natural backpressure: downloads slow down when disk is full, speed up as scans complete.
type DiskWatcher struct {
	budget  int64 // max bytes allowed for temp files (0 = unlimited)
	current int64 // current bytes in use by in-flight temp files
	mu      sync.Mutex
	// notify is signaled when space is freed. A larger buffer (rather than 1)
	// means concurrent Releases don't drop wakeups: previously a buffer of 1
	// could lose signals, forcing waiting Acquires to wait up to 500ms on the
	// periodic re-check. With a roomy buffer the re-check becomes a rare safety
	// net instead of the common path.
	notify  chan struct{}
}

// NewDiskWatcher creates a watcher with the given byte budget (0 = unlimited, no throttling).
func NewDiskWatcher(budgetBytes int64) *DiskWatcher {
	return &DiskWatcher{
		budget: budgetBytes,
		// 64 slots comfortably absorbs bursts from tens of concurrent scan
		// workers finishing within the same window; the periodic re-check still
		// exists as a backstop for pathological cases.
		notify: make(chan struct{}, 64),
	}
}

// Acquire reserves sizeHint bytes against the disk budget. It blocks until enough
// space is available or the context is cancelled. Pass sizeHint=0 to reserve a
// default per-slot size when the actual size is unknown.
// Returns the number of bytes actually reserved (may differ from sizeHint when using default).
func (dw *DiskWatcher) Acquire(ctx context.Context, sizeHint int64) (int64, error) {
	if dw.budget <= 0 {
		return 0, nil // unlimited budget
	}

	size := sizeHint
	if size <= 0 {
		// Default reservation: budget / 20 (assume up to 20 concurrent downloads)
		size = dw.budget / 20
		if size < 1024*1024 { // minimum 1MB reservation
			size = 1024 * 1024
		}
	}

	for {
		dw.mu.Lock()
		if dw.current+size <= dw.budget {
			dw.current += size
			dw.mu.Unlock()
			return size, nil
		}
		dw.mu.Unlock()

		// Wait for space to be freed or context cancellation. The notify channel
		// has a roomy buffer so a burst of Releases is unlikely to all be dropped;
		// the 50ms periodic re-check is just a backstop.
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("disk budget acquire cancelled: %w", ctx.Err())
		case <-dw.notify:
			// Space may have been freed, re-check
		case <-time.After(50 * time.Millisecond):
			// Periodic re-check in case notify was missed
		}
	}
}

// Release frees reserved bytes after a temp file is cleaned up.
// This signals any waiting downloaders that space may be available.
func (dw *DiskWatcher) Release(size int64) {
	if dw.budget <= 0 {
		return
	}

	dw.mu.Lock()
	dw.current -= size
	if dw.current < 0 {
		dw.current = 0
	}
	dw.mu.Unlock()

	// Non-blocking signal to wake up waiting Acquire calls. The buffer is large
	// enough that under normal load this never blocks; if it ever does, the
	// default drops the signal safely (Acquire's periodic re-check recovers).
	select {
	case dw.notify <- struct{}{}:
	default:
	}
}

// Update adjusts the reserved size after download completes (actual size may differ from hint).
func (dw *DiskWatcher) Update(oldSize, newSize int64) {
	if dw.budget <= 0 {
		return
	}

	dw.mu.Lock()
	dw.current -= oldSize
	dw.current += newSize
	if dw.current < 0 {
		dw.current = 0
	}
	dw.mu.Unlock()
}

// Current returns the current bytes in use.
func (dw *DiskWatcher) Current() int64 {
	dw.mu.Lock()
	defer dw.mu.Unlock()
	return dw.current
}

// Budget returns the total byte budget.
func (dw *DiskWatcher) Budget() int64 {
	return dw.budget
}
