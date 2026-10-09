// Package config loads the policy file.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"

	"cleanroom/internal/core"
)

// Config is a loaded policy and where it came from.
type Config struct {
	Policy core.Policy
	// Path is the absolute path of the policy file.
	Path string
	// Dir is the directory holding the policy file. Upstream and reader
	// commands run with it as their working directory, so a policy can
	// use paths relative to itself and work from any client.
	Dir string
}

// Load reads and validates a policy file. The file is JSON with comments
// and trailing commas allowed (HuJSON), because a security policy without
// comments is hard to review. Unknown keys are errors, so a misspelt
// setting cannot be silently ignored.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var p core.Policy
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Config{Policy: p, Path: abs, Dir: filepath.Dir(abs)}, nil
}
