package tts

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/config"
)

type Provider struct {
	cfg   config.TTSProvider
	name  string
	cache *Cache
}

func NewProvider(name string, cfg config.TTSProvider, cacheDir string) (*Provider, error) {
	cache, err := NewCache(cacheDir, 500)
	if err != nil {
		return nil, fmt.Errorf("init cache: %w", err)
	}
	return &Provider{cfg: cfg, name: name, cache: cache}, nil
}

// Generate synthesizes speech for the given text and returns the audio filename.
// Returns cached filename if available.
func (p *Provider) Generate(ctx context.Context, voice, text string) (string, error) {
	key := cacheKey(p.name, voice, text)

	// Check cache — return just the filename (not full path)
	if path, ok := p.cache.Get(key); ok {
		return filepath.Base(path), nil
	}

	filename := key + "." + p.cfg.Format
	outputPath := filepath.Join(p.cache.Dir(), filename)

	// Build command
	args := buildArgs(p.cfg.Args, map[string]string{
		"voice":  voice,
		"text":   text,
		"output": outputPath,
	})

	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tts %s: %w", p.name, err)
	}

	// Cache the result
	p.cache.Put(key, outputPath, p.cfg.Format)

	return filename, nil
}

func cacheKey(provider, voice, text string) string {
	h := md5.Sum([]byte(provider + ":" + voice + ":" + text))
	return fmt.Sprintf("%x", h)
}

func buildArgs(templates []string, vars map[string]string) []string {
	args := make([]string, len(templates))
	for i, tpl := range templates {
		arg := tpl
		for k, v := range vars {
			arg = strings.ReplaceAll(arg, "{"+k+"}", v)
		}
		args[i] = arg
	}
	return args
}
