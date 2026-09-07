package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

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
