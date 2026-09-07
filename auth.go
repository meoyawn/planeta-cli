package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

type Browser struct{ value sweetcookie.Browser }

func NewBrowserFromValue(value string) (Browser, error) {
	name := sweetcookie.Browser(strings.ToLower(strings.TrimSpace(value)))
	switch name {
	case sweetcookie.BrowserChrome, sweetcookie.BrowserChromium, sweetcookie.BrowserEdge,
		sweetcookie.BrowserBrave, sweetcookie.BrowserVivaldi, sweetcookie.BrowserOpera,
		sweetcookie.BrowserArc, sweetcookie.BrowserHelium, sweetcookie.BrowserDia,
		sweetcookie.BrowserComet, sweetcookie.BrowserAtlas, sweetcookie.BrowserWhale,
		sweetcookie.BrowserFirefox, sweetcookie.BrowserZen, sweetcookie.BrowserFloorp,
		sweetcookie.BrowserWaterfox, sweetcookie.BrowserLibreWolf, sweetcookie.BrowserSafari:
		return Browser{value: name}, nil
	default:
		return Browser{}, fmt.Errorf("unsupported browser %q; use chrome, chromium, edge, brave, vivaldi, opera, arc, helium, dia, comet, atlas, whale, firefox, zen, floorp, waterfox, librewolf, or safari", value)
	}
}

type storedCookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Domain   string     `json:"domain"`
	Path     string     `json:"path"`
	Expires  *time.Time `json:"expires,omitempty"`
	Secure   bool       `json:"secure"`
	HTTPOnly bool       `json:"http_only"`
}

type authStore struct {
	Browser    string         `json:"browser"`
	Profile    string         `json:"browser_profile,omitempty"`
	ImportedAt time.Time      `json:"imported_at"`
	Cookies    []storedCookie `json:"cookies"`
}

type authResult struct {
	Browser            string     `json:"browser"`
	Profile            string     `json:"browser_profile,omitempty"`
	ImportedAt         time.Time  `json:"imported_at"`
	CookieNames        []string   `json:"cookie_names"`
	CookieCount        int        `json:"cookie_count"`
	ClearanceExpiresAt *time.Time `json:"clearance_expires_at,omitempty"`
	Store              string     `json:"store"`
	HTTPRequests       int        `json:"http_requests"`
	Warnings           []string   `json:"warnings,omitempty"`
}

type cookieReader func(context.Context, sweetcookie.Options) (sweetcookie.Result, error)

func importBrowserCookies(ctx context.Context, browser Browser, profile, path string, read cookieReader) (*authResult, error) {
	if browser.value == "" {
		return nil, fmt.Errorf("browser is required; use --browser chrome (or your browser)")
	}
	opts := sweetcookie.Options{
		URL:      siteOrigin + "/",
		Names:    []string{"qrator_jsid", "qrator_jsid2", "city_id", "city_code", "region_id"},
		Browsers: []sweetcookie.Browser{browser.value},
		Mode:     sweetcookie.ModeFirst, Timeout: 30 * time.Second,
	}
	if profile != "" {
		opts.Profiles = map[sweetcookie.Browser]string{browser.value: profile}
	}
	result, err := read(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("read %s cookies: %w", browser.value, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := authStore{Browser: string(browser.value), Profile: profile, ImportedAt: time.Now().UTC()}
	hasClearance := false
	for _, cookie := range result.Cookies {
		domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
		if domain != "planetazdorovo.ru" || !isAnonymousCookie(cookie.Name) || cookie.Value == "" || (cookie.Path != "" && cookie.Path != "/") {
			continue
		}
		if cookie.Expires != nil && !cookie.Expires.After(store.ImportedAt) {
			continue
		}
		check := &http.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: "/"}
		if check.Valid() != nil {
			continue
		}
		expires := cookie.Expires
		if expires != nil {
			if _, err := expires.MarshalJSON(); err != nil {
				expires = nil
			}
		}
		store.Cookies = append(store.Cookies, storedCookie{
			Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: "/",
			Expires: expires, Secure: cookie.Secure, HTTPOnly: cookie.HTTPOnly,
		})
		if cookie.Name == "qrator_jsid" || cookie.Name == "qrator_jsid2" {
			hasClearance = true
		}
		if store.Profile == "" {
			store.Profile = cookie.Source.Profile
		}
	}
	if !hasClearance {
		detail := ""
		if len(result.Warnings) > 0 {
			detail = "; " + strings.Join(result.Warnings[:min(3, len(result.Warnings))], "; ")
		}
		return nil, fmt.Errorf("no unexpired anonymous Qrator cookies found in %s; open planetazdorovo.ru in that browser while signed out, allow its check to finish, and re-import; use --browser-profile for a different profile%s", browser.value, detail)
	}
	if err := atomicJSON(path, store); err != nil {
		return nil, fmt.Errorf("save imported cookies: %w", err)
	}
	summary := store.summary(path)
	summary.Warnings = result.Warnings
	return summary, nil
}

func (s *authStore) summary(path string) *authResult {
	result := &authResult{Browser: s.Browser, Profile: s.Profile, ImportedAt: s.ImportedAt, Store: path, CookieNames: []string{}}
	for _, cookie := range s.Cookies {
		result.CookieNames = append(result.CookieNames, cookie.Name)
		if (cookie.Name == "qrator_jsid" || cookie.Name == "qrator_jsid2") && cookie.Expires != nil {
			if result.ClearanceExpiresAt == nil || cookie.Expires.Before(*result.ClearanceExpiresAt) {
				result.ClearanceExpiresAt = cookie.Expires
			}
		}
	}
	slices.Sort(result.CookieNames)
	result.CookieCount = len(s.Cookies)
	return result
}

func loadAuth(path string) (*authStore, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return nil, fmt.Errorf("read auth store %q: %w", path, err)
	}
	var store authStore
	if len(data) > 64<<10 || json.Unmarshal(data, &store) != nil {
		return nil, fmt.Errorf("invalid auth store %q; run planeta auth import --browser chrome again", path)
	}
	return &store, nil
}

func (s *authStore) httpCookies() []*http.Cookie {
	var cookies []*http.Cookie
	for _, stored := range s.Cookies {
		if !isAnonymousCookie(stored.Name) || strings.TrimPrefix(strings.ToLower(stored.Domain), ".") != "planetazdorovo.ru" {
			continue
		}
		cookie := &http.Cookie{
			Name: stored.Name, Value: stored.Value, Domain: stored.Domain, Path: "/",
			Secure: stored.Secure, HttpOnly: stored.HTTPOnly,
		}
		if stored.Expires != nil {
			cookie.Expires = *stored.Expires
		}
		if cookie.Valid() == nil {
			cookies = append(cookies, cookie)
		}
	}
	return cookies
}

func loadClientCookies(explicit, authPath, legacyDir string) ([]*http.Cookie, error) {
	if explicit != "" {
		return readCookies(explicit)
	}
	store, err := loadAuth(authPath)
	if err == nil {
		return store.httpCookies(), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	path, err := locateCookieFile("", legacyDir, ".planeta-cookies")
	if err != nil {
		return nil, err
	}
	return readCookies(path)
}

// Temp-file + rename prevents readers from seeing partial JSON during concurrent
// searches/imports. Both auth and the public URL index use owner-only permissions.
func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode local state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".planeta-*")
	if err != nil {
		return fmt.Errorf("create state file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("write state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
