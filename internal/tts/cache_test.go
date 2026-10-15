package tts

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCacheInit(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "tts_cache_test")
	defer os.RemoveAll(dir)

	c, err := NewCache(dir, 10)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	if c.Dir() != dir {
		t.Errorf("Dir = %q, want %q", c.Dir(), dir)
	}
	if c.maxFiles != 10 {
		t.Errorf("maxFiles = %d, want 10", c.maxFiles)
	}
}

func TestCachePutGet(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "tts_cache_get_test")
	defer os.RemoveAll(dir)

	c, _ := NewCache(dir, 10)

	// Create a temp file to use as source
	src := filepath.Join(dir, "source.wav")
	os.WriteFile(src, []byte("audio_data"), 0644)

	c.Put("abc123", src, "wav")

	path, ok := c.Get("abc123")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if filepath.Base(path) != "abc123.wav" {
		t.Errorf("path = %q, expected abc123.wav", path)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("cached file does not exist")
	}

	// Get again should still work
	_, ok = c.Get("abc123")
	if !ok {
		t.Error("second get should still hit")
	}
}

func TestCacheGetMiss(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "tts_cache_miss_test")
	defer os.RemoveAll(dir)

	c, _ := NewCache(dir, 10)

	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestCacheEviction(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "tts_cache_evict_test")
	defer os.RemoveAll(dir)

	c, _ := NewCache(dir, 3)

	src := filepath.Join(dir, "source.wav")
	os.WriteFile(src, []byte("data"), 0644)

	// Insert 3 items
	c.Put("a", src, "wav")
	c.Put("b", src, "wav")
	c.Put("c", src, "wav")

	// Insert 4th — should evict "a" (oldest)
	c.Put("d", src, "wav")

	_, ok := c.Get("a")
	if ok {
		t.Error("a should have been evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("b should still be in cache")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("c should still be in cache")
	}
	if _, ok := c.Get("d"); !ok {
		t.Error("d should be in cache")
	}
}

func TestCacheLRUOrder(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "tts_cache_lru_test")
	defer os.RemoveAll(dir)

	c, _ := NewCache(dir, 3)

	src := filepath.Join(dir, "source.wav")
	os.WriteFile(src, []byte("data"), 0644)

	c.Put("a", src, "wav")
	c.Put("b", src, "wav")
	c.Put("c", src, "wav")

	// Access "a" — "b" becomes oldest
	c.Get("a")

	// Insert new — should evict "b" not "a"
	c.Put("d", src, "wav")

	if _, ok := c.Get("a"); !ok {
		t.Error("a should still be in cache (recently accessed)")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get("d"); !ok {
		t.Error("d should be in cache")
	}
}

func TestCacheExternalDeletion(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "tts_cache_del_test")
	defer os.RemoveAll(dir)

	c, _ := NewCache(dir, 10)

	src := filepath.Join(dir, "source.wav")
	os.WriteFile(src, []byte("data"), 0644)

	c.Put("xyz", src, "wav")

	// Verify it's cached
	path, ok := c.Get("xyz")
	if !ok {
		t.Fatal("expected cache hit")
	}

	// Delete the cached file externally
	os.Remove(path)

	// Get should now miss
	_, ok = c.Get("xyz")
	if ok {
		t.Error("expected miss after external deletion")
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "tts_cache_concurrent_test")
	defer os.RemoveAll(dir)

	c, _ := NewCache(dir, 100)

	src := filepath.Join(dir, "source.wav")
	os.WriteFile(src, []byte("data"), 0644)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := string(rune('A' + n%26))
			c.Put(key+string(rune('0'+n)), src, "wav")
		}(i)
	}
	wg.Wait()

	// Should not panic or corrupt
	if c.lru.Len() > 100 {
		t.Errorf("cache grew beyond maxFiles: %d", c.lru.Len())
	}
}
