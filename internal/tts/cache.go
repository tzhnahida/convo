package tts

import (
	"container/list"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Cache struct {
	mu       sync.Mutex
	dir      string
	maxFiles int
	items    map[string]*list.Element
	lru      *list.List
}

type cacheEntry struct {
	key    string
	path   string
	format string
}

func NewCache(dir string, maxFiles int) (*Cache, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	return &Cache{
		dir:      dir,
		maxFiles: maxFiles,
		items:    make(map[string]*list.Element),
		lru:      list.New(),
	}, nil
}

func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return "", false
	}

	entry := elem.Value.(*cacheEntry)
	if _, err := os.Stat(entry.path); os.IsNotExist(err) {
		// File was deleted externally
		c.lru.Remove(elem)
		delete(c.items, key)
		return "", false
	}

	c.lru.MoveToFront(elem)
	return entry.path, true
}

func (c *Cache) Put(key, sourcePath, format string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict oldest if at capacity (before adding)
	for c.lru.Len() >= c.maxFiles {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*cacheEntry)
		os.Remove(entry.path)
		c.lru.Remove(oldest)
		delete(c.items, entry.key)
	}

	// Move file to cache dir with MD5 name
	destPath := filepath.Join(c.dir, key+"."+format)
	if sourcePath != destPath {
		// If the file is already at the right place, don't copy
		if err := copyFile(sourcePath, destPath); err != nil {
			return
		}
	}

	elem := c.lru.PushFront(&cacheEntry{key: key, path: destPath, format: format})
	c.items[key] = elem
}

func (c *Cache) Dir() string {
	return c.dir
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
