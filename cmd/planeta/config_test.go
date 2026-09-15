package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfigPaths(t *testing.T) appPaths {
	t.Helper()
	dir := t.TempDir()
	return appPaths{
		config: filepath.Join(dir, "config", "config.json"),
		index:  filepath.Join(dir, "cache", "products"),
		auth:   filepath.Join(dir, "config", "auth.json"),
		legacy: filepath.Join(dir, "legacy"),
	}
}

func TestConfigPersistsOnlyCity(t *testing.T) {
	t.Parallel()
	paths := testConfigPaths(t)
	var output bytes.Buffer
	if err := runConfig(t.Context(), nil, &output, paths.config); err != nil || output.String() != "{\n  \"city\": \"\"\n}\n" {
		t.Fatalf("missing config must have no city: %s, %v", output.String(), err)
	}
	if _, err := os.Stat(paths.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("viewing config created a file: %v", err)
	}
	output.Reset()
	if err := runConfig(t.Context(), []string{"set", "city", " TEST-CITY "}, &output, paths.config); err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(output.Bytes(), &config); err != nil || len(config) != 1 || config["city"] != "test-city" {
		t.Fatalf("config must contain only the city slug: %s, %v", output.String(), err)
	}
	for path, mode := range map[string]os.FileMode{paths.config: 0600, filepath.Dir(paths.config): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("wrong permissions for %s: %v, %v", path, info, err)
		}
	}
	saved := output.String()
	output.Reset()
	if err := runConfig(t.Context(), nil, &output, paths.config); err != nil || output.String() != saved {
		t.Fatalf("config did not persist across invocations: %s, %v", output.String(), err)
	}
}

func TestInvalidConfigUpdatesPreserveSavedCity(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"set", "city", ""}, {"set", "city", " \t "}, {"set", "city", "../unsafe"},
		{"set", "city", "Тест\nгород"}, {"set", "city", strings.Repeat("x", 257)},
		{"set", "timeout", "60s"}, {"set", "city"}, {"set", "city", "one", "two"},
		{"set", "city", "Несуществующий город"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			paths := testConfigPaths(t)
			if err := atomicJSON(paths.config, userConfig{City: "saved-city"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(paths.config)
			if err != nil {
				t.Fatal(err)
			}
			if err := runConfig(t.Context(), args, io.Discard, paths.config); err == nil {
				t.Fatal("accepted invalid update")
			}
			after, err := os.ReadFile(paths.config)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed update changed the saved config: %v", err)
			}
		})
	}
}

func TestCanceledConfigUpdatePreservesSavedCity(t *testing.T) {
	t.Parallel()
	paths := testConfigPaths(t)
	if err := atomicJSON(paths.config, userConfig{City: "saved-city"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runConfig(ctx, []string{"set", "city", "other-city"}, io.Discard, paths.config); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
	config, err := loadConfig(paths.config)
	if err != nil || config.City != "saved-city" {
		t.Fatalf("canceled update changed config: %+v, %v", config, err)
	}
}

func TestMalformedConfigIsReported(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`null`, `[]`, `{"city":123}`, `{"city":"test-city","extra":true}`, `{"city":"../unsafe"}`, `{"city":" "}`, `{`, `{} {}`, `{} trailing`, strings.Repeat(" ", 65537)} {
		t.Run(body[:min(len(body), 70)], func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("malformed config was ignored or error omitted its path: %v", err)
			}
		})
	}
}

func TestSavedCityFlagPrecedence(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"search", "id"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			paths := testConfigPaths(t)
			if err := atomicJSON(paths.config, userConfig{City: "saved-city"}); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				args []string
				want string
			}{
				{[]string{"1"}, "saved-city"},
				{[]string{"--city", "other-city", "1"}, "other-city"},
				{[]string{"1", "--city=other-city"}, "other-city"},
				{[]string{"1", "--city="}, ""},
			} {
				flags := flag.NewFlagSet(command, flag.ContinueOnError)
				city := flags.String("city", "", "")
				if err := parseOptions(flags, test.args, command == "id"); err != nil {
					t.Fatal(err)
				}
				if err := applySavedCity(flags, paths.config); err != nil || *city != test.want {
					t.Fatalf("args=%v city=%q want=%q error=%v", test.args, *city, test.want, err)
				}
			}
		})
	}
}

func TestCommandsUseConfigWithoutImplicitCity(t *testing.T) {
	t.Parallel()
	paths := testConfigPaths(t)
	locatePaths := func() (appPaths, error) { return paths, nil }
	for _, args := range [][]string{{"search", "query"}, {"id", "1"}} {
		if err := runWithPaths(t.Context(), args, io.Discard, io.Discard, locatePaths); err == nil || !strings.Contains(err.Error(), "planeta config set city") {
			t.Fatalf("missing city did not fail locally: %v", err)
		}
	}
	if err := runWithPaths(t.Context(), []string{"config", "set", "city", "saved-city"}, io.Discard, io.Discard, locatePaths); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"id", "1"}, "in saved-city"},
		{[]string{"id", "1", "--city", "other-city"}, "in other-city"},
		{[]string{"search", "query", "--city="}, "city \"\" must not be empty"},
	} {
		if err := runWithPaths(t.Context(), test.args, io.Discard, io.Discard, locatePaths); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("config not applied: args=%v error=%v want=%s", test.args, err, test.want)
		}
	}
}

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
