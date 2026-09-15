package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steipete/sweetcookie"
)

func browserCookie(value string, expires time.Time) sweetcookie.Cookie {
	return sweetcookie.Cookie{Name: "qrator_jsid2", Value: value, Domain: "planetazdorovo.ru", Path: "/", Expires: &expires,
		Source: sweetcookie.Source{Browser: sweetcookie.BrowserChrome, Profile: "Your Chrome", StorePath: "/browser/Default/Cookies"}}
}

func TestFailedImportReturnsInstructionsWithoutPolling(t *testing.T) {
	t.Parallel()
	browser, err := NewBrowserFromValue("chrome")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	previous := []byte("previous store")
	if err := os.WriteFile(path, previous, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = importBrowserCookies(t.Context(), browser, "", path, func(_ context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
		calls++
		if calls > 2 {
			t.Fatal("failed import polled the browser instead of returning")
		}
		if opts.IncludeExpired != (calls == 2) {
			t.Fatal("expected one import read and one expiry diagnostic read")
		}
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie("expired-secret", time.Now().Add(-time.Hour))}}, nil
	})
	if err == nil || calls != 2 {
		t.Fatalf("unexpected import result: calls=%d err=%v", calls, err)
	}
	for _, expected := range []string{"expired at", "Reload", "planeta auth import --browser chrome"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("missing guidance %q: %v", expected, err)
		}
	}
	if strings.Contains(err.Error(), "wait") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error suggests waiting or exposes a cookie: %v", err)
	}
	data, readErr := os.ReadFile(path) // #nosec G304 -- This is the test's temporary auth store.
	if readErr != nil || !bytes.Equal(data, previous) {
		t.Fatalf("failed import changed saved authentication: %v", readErr)
	}
}

func TestExpiredAuthRequiresExplicitImport(t *testing.T) {
	t.Parallel()
	for _, browser := range []string{"chrome", "firefox"} {
		t.Run(browser, func(t *testing.T) {
			t.Parallel()
			paths := appPaths{auth: filepath.Join(t.TempDir(), "auth.json"), legacy: t.TempDir()}
			past := time.Now().Add(-time.Hour)
			old := authStore{Browser: browser, Profile: "Default", ImportedAt: past,
				Cookies: []storedCookie{{Name: "qrator_jsid2", Value: "expired-secret", Domain: "planetazdorovo.ru", Path: "/", Expires: &past}}}
			if err := atomicJSON(paths.auth, old); err != nil {
				t.Fatal(err)
			}
			session, cookies, err := loadAuthSession(paths, "")
			if err == nil || session != nil || cookies != nil || !strings.Contains(err.Error(), "planeta auth import --browser "+browser) {
				t.Fatalf("expired auth did not require explicit import: %v", err)
			}
			if strings.Contains(err.Error(), "wait") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error suggests waiting or exposes a cookie: %v", err)
			}
			stored, err := loadAuth(paths.auth)
			if err != nil || !stored.ImportedAt.Equal(past) {
				t.Fatalf("expired auth was changed: %v", err)
			}
		})
	}
}

func TestValidAuthLoadsSavedCookies(t *testing.T) {
	t.Parallel()
	paths := appPaths{auth: filepath.Join(t.TempDir(), "auth.json"), legacy: t.TempDir()}
	future := time.Now().Add(time.Hour)
	store := authStore{Browser: "chrome", Cookies: []storedCookie{
		{Name: "qrator_jsid2", Value: "valid", Domain: "planetazdorovo.ru", Path: "/", Expires: &future},
	}}
	if err := atomicJSON(paths.auth, store); err != nil {
		t.Fatal(err)
	}
	session, cookies, err := loadAuthSession(paths, "")
	if err != nil || session == nil || len(cookies) != 1 || cookies[0].Value != "valid" {
		t.Fatalf("saved auth was not loaded: %v", err)
	}
}

func TestLegacyOverrideDoesNotLoadManagedAuth(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cookies")
	writeTestCookieFile(t, path)
	session, cookies, err := loadAuthSession(appPaths{}, path)
	if err != nil || session != nil || len(cookies) != 1 {
		t.Fatalf("legacy override was not preserved: %v", err)
	}
}

func TestCookieChallengeStopsWithImportInstructions(t *testing.T) {
	t.Parallel()
	for _, challengePath := range []string{"/test-city/", "/search/", "/test-city/catalog/test-1/"} {
		t.Run(challengePath, func(t *testing.T) {
			t.Parallel()
			requests := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path == challengePath {
					w.WriteHeader(http.StatusUnauthorized)
					writeTestResponse(t, w, `<script src="/__qrator/ldr.js"></script>`)
					return
				}
				if !selectMockCity(t, w, r) {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			})
			var err error
			if strings.Contains(challengePath, "/catalog/") {
				_, err = c.Detail(t.Context(), "1", "test-city", c.base.String()+challengePath)
			} else {
				_, err = c.Search(t.Context(), "кора осины", "test-city", 1)
			}
			wantRequests := 2
			if challengePath == "/test-city/" {
				wantRequests = 1
			}
			if !errors.Is(err, errBrowserCheck) || requests != wantRequests || c.requests != wantRequests {
				t.Fatalf("challenge did not stop immediately: requests=%d err=%v", requests, err)
			}
			if !strings.Contains(err.Error(), "planeta auth import --browser chrome") || strings.Contains(err.Error(), "wait") {
				t.Fatalf("challenge instructions are not actionable: %v", err)
			}
		})
	}
}

func TestResponseCookiesPersistOnlyAnonymousRenewals(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "auth.json")
	store, warnings, err := readBrowserAuth(t.Context(), Browser{value: sweetcookie.BrowserChrome}, "", func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie("old", time.Now().Add(time.Minute))}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveBrowserAuth(path, store, warnings); err != nil {
		t.Fatal(err)
	}
	session := &authSession{path: path, store: store}
	past := time.Now().Add(-time.Hour)
	if err := session.saveCookies([]*http.Cookie{
		{Name: "qrator_jsid2", Value: "renewed", Path: "/", Expires: past, MaxAge: 3600}, // #nosec G124 -- Synthetic response cookies exercise persistence and filtering.
		{Name: "PHPSESSID", Value: "private-account", Path: "/"},                         // #nosec G124 -- Verify that account cookies are excluded regardless of flags.
		{Name: "qrator_jsid", Value: "other-host", Domain: "other.example", Path: "/"},   // #nosec G124 -- Verify that unrelated domains are excluded regardless of flags.
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := loadAuth(path)
	if err != nil || len(updated.Cookies) != 1 || updated.Cookies[0].Value != "renewed" || !updated.Cookies[0].Expires.After(time.Now().Add(59*time.Minute)) {
		t.Fatalf("renewal was lost or private cookies persisted: %v", err)
	}
	if err := session.saveCookies([]*http.Cookie{{Name: "qrator_jsid2", Path: "/", MaxAge: -1}}); err != nil { // #nosec G124 -- Synthetic deletion instruction for the local cookie store.
		t.Fatal(err)
	}
	updated, err = loadAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.hasClearance(time.Now()) {
		t.Fatal("cookie deletion was not persisted")
	}
	updated.ImportedAt = updated.ImportedAt.Add(time.Hour)
	if err := atomicJSON(path, updated); err != nil {
		t.Fatal(err)
	}
	if err := session.saveCookies([]*http.Cookie{{Name: "qrator_jsid2", Value: "stale-command", Path: "/"}}); err != nil { // #nosec G124 -- Synthetic renewal from an older local command.
		t.Fatal(err)
	}
	updated, err = loadAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Cookies) != 0 {
		t.Fatal("an older command overwrote a newer import")
	}
}

func TestImportDoesNotCombineClearanceFromDifferentProfiles(t *testing.T) {
	t.Parallel()
	store, _, err := readBrowserAuth(t.Context(), Browser{value: sweetcookie.BrowserChrome}, "", func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		primary := browserCookie("primary", time.Now().Add(time.Hour))
		other := browserCookie("other-profile", time.Now().Add(time.Hour))
		other.Source.StorePath, other.Name = "/browser/Profile 1/Cookies", "qrator_jsid"
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{primary, other}}, nil
	})
	if err != nil || len(store.Cookies) != 1 || store.Cookies[0].Value != "primary" {
		t.Fatalf("clearance from unrelated profiles was mixed: %v", err)
	}
}

func TestRemovedAuthWaitFlagsFailWithoutBrowserAccess(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"auth", "import", "--browser", "chrome", "--wait", "2m"}, {"search", "test", "--auth-wait", "2m"}, {"search", "test", "--page", "0"}} {
		var output bytes.Buffer
		if err := run(t.Context(), args, &output, &output); err == nil {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
}
