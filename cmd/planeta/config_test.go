package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestCookieFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("qrator_jsid2=anonymous-clearance"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCookieDiscoveryWorksOutsideProject(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	global := filepath.Join(configDir, "planeta", "cookies")
	writeTestCookieFile(t, global)
	for _, cwd := range []string{t.TempDir(), t.TempDir()} {
		got, err := locateCookieFile("", configDir, filepath.Join(cwd, ".planeta-cookies"))
		if err != nil || got != global {
			t.Fatalf("cookie lookup from %q: got %q, error %v; want %q", cwd, got, err, global)
		}
		cookies, err := readCookies(got)
		if err != nil || len(cookies) != 1 {
			t.Fatalf("global cookies were not loaded: count=%d error=%v", len(cookies), err)
		}
	}
}

func TestCookieFilePrecedence(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	global := filepath.Join(configDir, "planeta", "cookies")
	local := filepath.Join(t.TempDir(), ".planeta-cookies")
	writeTestCookieFile(t, global)
	writeTestCookieFile(t, local)
	explicit := filepath.Join(t.TempDir(), "explicit-cookies")
	for _, test := range []struct{ name, override, config, want string }{
		{"explicit override", explicit, configDir, explicit},
		{"global overrides local", "", configDir, global},
		{"legacy fallback", "", t.TempDir(), local},
		{"no user config directory", "", "", local},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := locateCookieFile(test.override, test.config, local)
			if err != nil || got != test.want {
				t.Fatalf("got %q, error %v; want %q", got, err, test.want)
			}
		})
	}
	// A missing explicit path must fail when read, never use another file silently.
	if _, err := readCookies(explicit); err == nil {
		t.Fatal("missing explicit cookie file was accepted")
	}
}

func TestMissingCookieFilesStillAllowAnonymousAttempt(t *testing.T) {
	t.Parallel()
	got, err := locateCookieFile("", t.TempDir(), filepath.Join(t.TempDir(), ".planeta-cookies"))
	if err != nil || got != "" {
		t.Fatalf("got %q, error %v; want no cookie file", got, err)
	}
}
