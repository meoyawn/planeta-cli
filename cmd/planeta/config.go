package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type userConfig struct {
	City string `json:"city"`
}

func validateCity(city string) error {
	if city == "" {
		return fmt.Errorf("city %q must not be empty; run planeta config set city <slug> or pass --city", city)
	}
	if len(city) > 256 || !citySlug.MatchString(city) {
		return fmt.Errorf("invalid city %.100q: expected a lowercase city URL slug of at most 256 bytes", city)
	}
	return nil
}

func loadConfig(path string) (config userConfig, err error) {
	f, err := os.Open(path) // #nosec G304 -- Path is the per-user preferences file.
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("open config %q: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close config %q: %w", path, closeErr))
		}
	}()
	const maxConfigBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return config, fmt.Errorf("read config %q: %w", path, err)
	}
	if len(data) > maxConfigBytes || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return config, fmt.Errorf("config %q must be a JSON object of at most %d bytes", path, maxConfigBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode config %q: %w", path, err)
	}
	if !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return config, fmt.Errorf("config %q must contain exactly one JSON object", path)
	}
	if config.City != "" {
		if err := validateCity(config.City); err != nil {
			return config, fmt.Errorf("invalid config %q: %w", path, err)
		}
	}
	return config, nil
}

func applySavedCity(flags *flag.FlagSet, path string) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	explicit := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "city" {
			explicit = true
		}
	})
	if !explicit {
		return flags.Set("city", config.City)
	}
	return nil
}

func runConfig(ctx context.Context, args []string, stdout io.Writer, path string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if len(args) != 0 && (len(args) != 3 || args[0] != "set") {
		return fmt.Errorf("config expects no arguments or: planeta config set city <slug>")
	}
	config, err := loadConfig(path)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return writeJSON(stdout, config)
	}
	if args[1] != "city" {
		return fmt.Errorf("unknown config key %q; use city", args[1])
	}
	config.City = strings.ToLower(strings.TrimSpace(args[2]))
	if err := validateCity(config.City); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := atomicJSON(path, config); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return writeJSON(stdout, config)
}

// The per-user file takes precedence over the legacy working-directory file so
// an installed CLI uses the same credentials from any directory.
func locateCookieFile(explicit, configDir, localPath string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	var candidates []string
	if configDir != "" {
		candidates = append(candidates, filepath.Join(configDir, "planeta", "cookies"))
	}
	candidates = append(candidates, localPath)
	for _, path := range candidates {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect cookie file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("cookie file %q must be a regular file", path)
		}
		return filepath.Abs(path)
	}
	return "", nil
}
