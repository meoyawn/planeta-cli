package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

func isClearanceCookie(name string) bool {
	return name == "qrator_jsid" || name == "qrator_jsid2"
}

// Browser cookies are read from disk only. Failed imports report how to refresh them.
type browserCookieError struct {
	browser   Browser
	profile   string
	storePath string
	expiredAt *time.Time
	warnings  []string
}

func (e *browserCookieError) inspect(cookies []sweetcookie.Cookie, now time.Time) {
	for _, cookie := range cookies {
		if !isAnonymousCookie(cookie.Name) || strings.TrimPrefix(strings.ToLower(cookie.Domain), ".") != "planetazdorovo.ru" {
			continue
		}
		if e.profile == "" {
			e.profile = cookie.Source.Profile
		}
		if e.storePath == "" {
			e.storePath = cookie.Source.StorePath
		}
		if isClearanceCookie(cookie.Name) && cookie.Expires != nil && !cookie.Expires.After(now) &&
			(e.expiredAt == nil || cookie.Expires.After(*e.expiredAt)) {
			e.expiredAt = cookie.Expires
			if cookie.Source.Profile != "" {
				e.profile = cookie.Source.Profile
			}
			if cookie.Source.StorePath != "" {
				e.storePath = cookie.Source.StorePath
			}
		}
	}
}

func (e *browserCookieError) reason() string {
	var message strings.Builder
	fmt.Fprintf(&message, "No usable Planeta site-verification cookie saved by %s", e.browser.value)
	if e.profile != "" {
		fmt.Fprintf(&message, " (profile %q)", e.profile)
	}
	message.WriteString(".\n")
	if e.expiredAt != nil {
		fmt.Fprintf(&message, "The last saved clearance expired at %s.\n", e.expiredAt.Local().Format(time.RFC1123))
	}
	if e.storePath != "" {
		fmt.Fprintf(&message, "Cookie file: %s\n", e.storePath)
	}
	for _, warning := range e.warnings[:min(3, len(e.warnings))] {
		fmt.Fprintf(&message, "Browser reader: %s\n", warning)
	}
	return message.String()
}

func (e *browserCookieError) Error() string {
	return e.reason() + fmt.Sprintf("Reload %s/ in %s, then run: planeta auth import --browser %s", siteOrigin, e.browser.value, e.browser.value)
}

type authSession struct {
	path  string
	store *authStore
}

func loadAuthSession(paths appPaths, explicit string) (*authSession, []*http.Cookie, error) {
	cookies, err := loadClientCookies(explicit, paths.auth, paths.legacy)
	if err != nil || explicit != "" {
		return nil, cookies, err
	}
	session := &authSession{path: paths.auth}
	store, err := loadAuth(paths.auth)
	if errors.Is(err, os.ErrNotExist) {
		return session, cookies, nil
	}
	if err != nil {
		return nil, nil, err
	}
	session.store = store
	browser, err := NewBrowserFromValue(store.Browser)
	if err != nil {
		return nil, nil, err
	}
	if !store.hasClearance(time.Now()) {
		return nil, nil, fmt.Errorf("saved Planeta site-verification cookie has expired; run planeta auth import --browser %s", browser.value)
	}
	return session, cookies, err
}

func (s *authStore) hasClearance(now time.Time) bool {
	for _, cookie := range s.httpCookies() {
		if isClearanceCookie(cookie.Name) && cookie.Value != "" && (cookie.Expires.IsZero() || cookie.Expires.After(now)) {
			return true
		}
	}
	return false
}

// Keep the expiry extensions returned by Qrator instead of discarding them at
// process exit. Exclude account cookies and skip imports observed after this
// command started.
func (s *authSession) saveCookies(cookies []*http.Cookie) error {
	if s.store == nil || len(cookies) == 0 {
		return nil
	}
	latest, err := loadAuth(s.path)
	if err != nil {
		return err
	}
	if !latest.ImportedAt.Equal(s.store.ImportedAt) {
		return nil
	}
	changed := false
	now := time.Now().UTC()
	for _, cookie := range cookies {
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if !isAnonymousCookie(cookie.Name) || (domain != "" && domain != "planetazdorovo.ru") ||
			(cookie.Path != "" && cookie.Path != "/") || cookie.Valid() != nil {
			continue
		}
		changed = true
		kept := latest.Cookies[:0]
		for _, old := range latest.Cookies {
			if old.Name != cookie.Name {
				kept = append(kept, old)
			}
		}
		latest.Cookies = kept
		if cookie.MaxAge < 0 || (cookie.MaxAge == 0 && !cookie.Expires.IsZero() && !cookie.Expires.After(now)) {
			continue
		}
		var expires *time.Time
		if cookie.MaxAge > 0 {
			value := now.Add(time.Duration(cookie.MaxAge) * time.Second)
			expires = &value
		} else if !cookie.Expires.IsZero() {
			value := cookie.Expires.UTC()
			expires = &value
		}
		latest.Cookies = append(latest.Cookies, storedCookie{Name: cookie.Name, Value: cookie.Value, Domain: "planetazdorovo.ru", Path: "/", Expires: expires, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly})
	}
	if changed {
		if err := atomicJSON(s.path, latest); err != nil {
			return fmt.Errorf("save renewed site cookies: %w", err)
		}
		s.store = latest
	}
	return nil
}
