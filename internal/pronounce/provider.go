package pronounce

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/config"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// Result is the JSON output expected from a pronunciation assessment binary.
type Result struct {
	Score    float64        `json:"score"`
	Phonemes []PhonemeScore `json:"phonemes"`
	Detail   string         `json:"detail,omitempty"`
}

// PhonemeScore is per-phoneme pronunciation quality.
type PhonemeScore struct {
	Phoneme string  `json:"phoneme"`
	Score   float64 `json:"score"`
}

// Provider dispatches pronunciation assessment to an external binary.
type Provider struct {
	cfg  config.PronounceProvider
	name string
}

// NewProvider creates a pronunciation assessment provider.
func NewProvider(name string, cfg config.PronounceProvider) *Provider {
	return &Provider{cfg: cfg, name: name}
}

// Assess runs the external binary to evaluate pronunciation of targetText
// in the audio file at audioPath.
func (p *Provider) Assess(ctx context.Context, audioPath, targetText string) (*Result, error) {
	log.Debug("pronounce", "assess: provider=%s audio=%s", p.name, audioPath)

	args := buildPronounceArgs(p.cfg.Args, audioPath, targetText)

	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if errMsg == "" {
			errMsg = stdout.String()
		}
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		return nil, fmt.Errorf("pronounce %s: %w (output: %s)", p.name, err, errMsg)
	}

	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("pronounce %s parse output: %w", p.name, err)
	}

	log.Debug("pronounce", "assess result: score=%.2f", result.Score)
	return &result, nil
}

func buildPronounceArgs(templates []string, audioPath, targetText string) []string {
	args := make([]string, len(templates))
	for i, tpl := range templates {
		arg := tpl
		arg = strings.ReplaceAll(arg, "{audio}", audioPath)
		arg = strings.ReplaceAll(arg, "{text}", targetText)
		args[i] = arg
	}
	return args
}
