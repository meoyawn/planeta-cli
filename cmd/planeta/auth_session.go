package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/steipete/sweetcookie"
)

func isClearanceCookie(name string) bool {
	return name == "qrator_jsid" || name == "qrator_jsid2"
}

// Browser cookies are read from disk only. A working browser tab can have newer
// cookies in memory, so let the user refresh it while we wait for a saved copy.
type browserCookieError struct {
	browser   Browser
	profile   string
	storePath string
	expiredAt *time.Time
	unchanged bool
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
	if e.unchanged {
		message.WriteString("The site rejected the saved cookie; a refreshed cookie has not reached the browser's cookie file yet.\n")
	} else if e.expiredAt != nil {
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

func (e *browserCookieError) help() string {
	message := fmt.Sprintf("Please reload %s/ in your existing, regular %s window.\nWait until the catalog appears and leave the tab open. No need to sign out.\nThe browser may need a few seconds to save its refreshed cookies to disk.\n", siteOrigin, e.browser.value)
	if e.browser.value == sweetcookie.BrowserChrome || e.browser.value == sweetcookie.BrowserChromium {
		message += "If you use another profile, copy Profile Path from chrome://version and pass it to --browser-profile.\n"
	} else {
		message += "If you use another profile, select it with --browser-profile. Private-window cookies cannot be imported from disk.\n"
	}
	return message
}

func (e *browserCookieError) Error() string {
	return e.reason() + "\n" + e.help() + fmt.Sprintf("Then run: planeta auth import --browser %s --wait 2m", e.browser.value)
}

type authImporter struct {
	browser  Browser
	profile  string
	path     string
	read     cookieReader
	wait     time.Duration
	interval time.Duration
	output   io.Writer
}

func (a authImporter) importCookies(ctx context.Context, rejected []*http.Cookie) (*authStore, []string, error) {
	if a.wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.wait)
		defer cancel()
	}
	interval := a.interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	output := a.output
	if output == nil {
		output = io.Discard
	}
	var lastProblem *browserCookieError
	for {
		store, warnings, err := readBrowserAuth(ctx, a.browser, a.profile, a.read)
		if err == nil && sameClearance(store.httpCookies(), rejected) {
			err = &browserCookieError{browser: a.browser, profile: store.Profile, storePath: store.ProfilePath, unchanged: true}
		}
		if err == nil {
			if _, err := saveBrowserAuth(a.path, store, warnings); err != nil {
				return nil, nil, err
			}
			if lastProblem != nil {
				if _, err := fmt.Fprintln(output, "Fresh cookies saved. Continuing."); err != nil {
					return nil, nil, fmt.Errorf("write cookie import progress: %w", err)
				}
			}
			return store, warnings, nil
		}
		var problem *browserCookieError
		if !errors.As(err, &problem) || a.wait == 0 {
			return nil, nil, err
		}
		if lastProblem == nil {
			if _, err := fmt.Fprint(output, problem.reason(), "\n", problem.help()); err != nil {
				return nil, nil, fmt.Errorf("write cookie import guidance: %w", err)
			}
			if _, err := fmt.Fprintf(output, "I'll check the saved cookies every %s for up to %s and continue automatically. Ctrl-C cancels.\n", interval, a.wait); err != nil {
				return nil, nil, fmt.Errorf("write cookie import progress: %w", err)
			}
		}
		lastProblem = problem
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, fmt.Errorf("stopped waiting for %s cookies: %w\n%w", a.browser.value, ctx.Err(), lastProblem)
		case <-timer.C:
		}
	}
}

func sameClearance(cookies, rejected []*http.Cookie) bool {
	values := make(map[string]string)
	for _, cookie := range rejected {
		if isClearanceCookie(cookie.Name) {
			values[cookie.Name] = cookie.Value
		}
	}
	if len(values) == 0 {
		return false
	}
	count := 0
	for _, cookie := range cookies {
		if isClearanceCookie(cookie.Name) {
			count++
			if values[cookie.Name] != cookie.Value {
				return false
			}
		}
	}
	return count == len(values)
}

func defaultAuthWait(stderr io.Writer) time.Duration {
	file, ok := stderr.(*os.File)
	if ok && isatty.IsTerminal(file.Fd()) && isatty.IsTerminal(os.Stdin.Fd()) {
		return 90 * time.Second
	}
	return 0
}

type authSession struct {
	importer authImporter
	store    *authStore
}

func loadAuthSession(ctx context.Context, paths appPaths, explicit string, wait time.Duration, output io.Writer, read cookieReader) (*authSession, []*http.Cookie, error) {
	cookies, err := loadClientCookies(explicit, paths.auth, paths.legacy)
	if err != nil || explicit != "" {
		return nil, cookies, err
	}
	browser := Browser{value: sweetcookie.BrowserChrome}
	session := &authSession{importer: authImporter{browser: browser, path: paths.auth, wait: wait, output: output, read: read}}
	store, err := loadAuth(paths.auth)
	if errors.Is(err, os.ErrNotExist) {
		return session, cookies, nil
	}
	if err != nil {
		return nil, nil, err
	}
	session.store = store
	session.importer.browser, err = NewBrowserFromValue(store.Browser)
	if err != nil {
		return nil, nil, err
	}
	// Older auth stores contain a display name only. Rediscover those profiles
	// instead of mistaking "Your Chrome" for an actual profile directory.
	session.importer.profile = store.ProfilePath
	if !store.hasClearance(time.Now()) {
		cookies, err = session.refresh(ctx, nil)
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

func (s *authSession) refresh(ctx context.Context, rejected []*http.Cookie) ([]*http.Cookie, error) {
	store, _, err := s.importer.importCookies(ctx, rejected)
	if err != nil {
		return nil, err
	}
	s.store = store
	s.importer.profile = store.ProfilePath
	return store.httpCookies(), nil
}

// Keep the expiry extensions returned by Qrator instead of discarding them at
// process exit. Exclude account cookies and skip imports observed after this
// command started.
func (s *authSession) saveCookies(cookies []*http.Cookie) error {
	if s.store == nil || len(cookies) == 0 {
		return nil
	}
	latest, err := loadAuth(s.importer.path)
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
		if err := atomicJSON(s.importer.path, latest); err != nil {
			return fmt.Errorf("save renewed site cookies: %w", err)
		}
		s.store = latest
	}
	return nil
}
