package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceInterval collapses a burst of filesystem events (many editors
// write a config file via temp-file-then-rename, which fires more than
// one event) into a single reload.
const debounceInterval = 300 * time.Millisecond

// watchConfigFile sends on trigger whenever path changes on disk, debounced,
// until ctx is canceled. It watches path's directory rather than the file
// itself - fsnotify doesn't reliably keep watching a file across an
// atomic rename-replace, but the directory entry survives that.
func watchConfigFile(ctx context.Context, path string, trigger chan<- struct{}, log *slog.Logger) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error("fsnotify: failed to create watcher", "error", err)
		return
	}
	defer watcher.Close()

	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		log.Error("fsnotify: failed to watch config directory", "dir", dir, "error", err)
		return
	}
	base := filepath.Base(path)

	var debounce *time.Timer
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) != base {
				continue
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(debounceInterval, func() {
				select {
				case trigger <- struct{}{}:
				case <-ctx.Done():
				}
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Error("fsnotify: watcher error", "error", err)
		}
	}
}
