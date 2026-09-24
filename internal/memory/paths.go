package memory

import "path/filepath"

// The store's layout. Every path is derived from the store directory, so nothing
// in this package needs to know where that directory comes from.

func ProjectsDir(store string) string { return filepath.Join(store, "projects") }

func ProjectDir(store, slug string) string { return filepath.Join(store, "projects", slug) }

func IndexPath(store, slug string) string {
	return filepath.Join(store, "projects", slug, "INDEX.md")
}

func PendingPath(store, slug string) string {
	return filepath.Join(store, "projects", slug, "pending.md")
}

func MemoriesDir(store string) string { return filepath.Join(store, "memories") }

func MemoryPath(store, id string) string {
	return filepath.Join(store, "memories", id+".md")
}

func SharedDir(store string) string { return filepath.Join(store, "shared") }
