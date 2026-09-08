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

func testAuthImporter(t *testing.T, read cookieReader) authImporter {
	t.Helper()
	browser, err := NewBrowserFromValue("chrome")
	if err != nil {
		t.Fatal(err)
	}
	return authImporter{browser: browser, path: filepath.Join(t.TempDir(), "auth.json"), read: read, interval: time.Millisecond}
}

func TestImportWaitsForDiskFlushAndExplainsWhatToDo(t *testing.T) {
	t.Parallel()
	calls := 0
	importer := testAuthImporter(t, func(_ context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
		calls++
		if opts.URL != siteOrigin+"/" || len(opts.Names) != 5 || opts.AllowAllHosts {
			t.Fatal("import widened its host or cookie scope")
		}
		if calls <= 2 {
			return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie("expired-secret", time.Now().Add(-time.Hour))}}, nil
		}
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie("fresh-secret", time.Now().Add(time.Hour))}}, nil
	})
	var output bytes.Buffer
	importer.wait, importer.output = time.Second, &output
	store, _, err := importer.importCookies(t.Context(), nil)
	if err != nil || calls != 3 || store.Cookies[0].Value != "fresh-secret" {
		t.Fatalf("delayed disk flush was not recovered: calls=%d err=%v", calls, err)
	}
	for _, expected := range []string{"Your Chrome", "/browser/Default/Cookies", "expired at", "Please reload", "No need to sign out", "chrome://version", "continue automatically", "Fresh cookies saved. Continuing."} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("missing guidance %q: %s", expected, output.String())
		}
	}
	if strings.Count(output.String(), "Please reload") != 1 || strings.Contains(output.String(), "secret") {
		t.Fatal("prompt repeated or exposed a cookie value")
	}
	if store.ProfilePath != "/browser/Default/Cookies" || store.Profile != "Your Chrome" {
		t.Fatal("profile display name and reusable path were not kept separately")
	}
}

func TestImportWaitZeroAndCancellationPreserveStore(t *testing.T) {
	t.Parallel()
	for _, wait := range []time.Duration{0, time.Hour} {
		t.Run(wait.String(), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			importer := testAuthImporter(t, func(_ context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
				calls++
				if wait > 0 && opts.IncludeExpired {
					cancel()
				}
				return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie("expired-secret", time.Now().Add(-time.Hour))}}, nil
			})
			importer.wait = wait
			previous := []byte("previous store")
			if err := os.WriteFile(importer.path, previous, 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err := importer.importCookies(ctx, nil)
			if err == nil || calls != 2 {
				t.Fatalf("unexpected read loop: calls=%d err=%v", calls, err)
			}
			if wait > 0 && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if wait == 0 && (!strings.Contains(err.Error(), "--wait 2m") || !strings.Contains(err.Error(), "expired at")) {
				t.Fatalf("noninteractive error has no recovery instructions: %v", err)
			}
			data, readErr := os.ReadFile(importer.path) // #nosec G304 -- This is the test's temporary auth store.
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(data, previous) || strings.Contains(err.Error(), "secret") {
				t.Fatal("failed import overwrote state or exposed a cookie")
			}
		})
	}
}

func TestImportTimeoutIncludesNextStep(t *testing.T) {
	t.Parallel()
	importer := testAuthImporter(t, func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{}, nil
	})
	importer.wait, importer.interval = time.Millisecond, time.Hour
	_, _, err := importer.importCookies(t.Context(), nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "--wait 2m") {
		t.Fatalf("timeout was not actionable: %v", err)
	}
}

func TestRejectedCookieMustChangeBeforeRetry(t *testing.T) {
	t.Parallel()
	for _, wait := range []time.Duration{0, time.Second} {
		t.Run(wait.String(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			importer := testAuthImporter(t, func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
				calls++
				value := "rejected-secret"
				if calls > 1 {
					value = "new-secret"
				}
				return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie(value, time.Now().Add(time.Hour))}}, nil
			})
			importer.wait = wait
			store, _, err := importer.importCookies(t.Context(), []*http.Cookie{{Name: "qrator_jsid2", Value: "rejected-secret"}}) // #nosec G124 -- Synthetic rejected cookie for a local comparison.
			if wait == 0 {
				if err == nil || calls != 1 || !strings.Contains(err.Error(), "site rejected") {
					t.Fatalf("accepted unchanged cookie: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 2 || store.Cookies[0].Value != "new-secret" {
				t.Fatalf("did not wait for changed clearance: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestExpiredAuthReimportsSameProfileBeforeHTTP(t *testing.T) {
	t.Parallel()
	for _, profilePath := range []string{"", "/browser/Default/Cookies"} {
		t.Run(profilePath, func(t *testing.T) {
			t.Parallel()
			paths := appPaths{auth: filepath.Join(t.TempDir(), "auth.json"), legacy: t.TempDir()}
			past := time.Now().Add(-time.Hour)
			old := authStore{Browser: "chrome", Profile: "Your Chrome", ProfilePath: profilePath, ImportedAt: past,
				Cookies: []storedCookie{{Name: "qrator_jsid2", Value: "expired", Domain: "planetazdorovo.ru", Path: "/", Expires: &past}}}
			if err := atomicJSON(paths.auth, old); err != nil {
				t.Fatal(err)
			}
			reads := 0
			session, cookies, err := loadAuthSession(t.Context(), paths, "", 0, nil, func(_ context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
				reads++
				if opts.Profiles[sweetcookie.BrowserChrome] != profilePath {
					t.Fatal("used a display name as a directory or lost the saved profile path")
				}
				return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie("fresh", time.Now().Add(time.Hour))}}, nil
			})
			if err != nil || reads != 1 || cookies[0].Value != "fresh" || session.store.ProfilePath != "/browser/Default/Cookies" {
				t.Fatalf("expired store was not refreshed: reads=%d err=%v", reads, err)
			}
			_, _, err = loadAuthSession(t.Context(), paths, "", 0, nil, func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
				t.Fatal("read the browser unnecessarily with fresh managed clearance")
				return sweetcookie.Result{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyOverrideDoesNotReadBrowser(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cookies")
	writeTestCookieFile(t, path)
	session, cookies, err := loadAuthSession(t.Context(), appPaths{}, path, time.Hour, nil, func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		t.Fatal("explicit cookie file read browser cookies")
		return sweetcookie.Result{}, nil
	})
	if err != nil || session != nil || len(cookies) != 1 {
		t.Fatalf("legacy override was not preserved: %v", err)
	}
}

func TestCookieRefreshRetriesOnlyChallengedRequestAndKeepsCity(t *testing.T) {
	t.Parallel()
	for _, challengePath := range []string{"/kazan/", "/search/", "/kazan/catalog/test-1/"} {
		t.Run(challengePath, func(t *testing.T) {
			t.Parallel()
			refreshed, requests := 0, 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path == challengePath && refreshed == 0 {
					w.WriteHeader(http.StatusUnauthorized)
					writeTestResponse(t, w, `<script src="/__qrator/ldr.js"></script>`)
					return
				}
				if selectMockCity(t, w, r) {
					return
				}
				city, err := r.Cookie("city_code")
				if err != nil || city.Value != "kazan" {
					t.Error("refresh changed the selected city")
				}
				clearance, err := r.Cookie("qrator_jsid2")
				if err != nil || clearance.Value != "fresh" {
					t.Error("retry did not use refreshed cookies")
				}
				if strings.Contains(challengePath, "/catalog/") {
					writeTestResponse(t, w, `<div class="product-detail" data-id="1"><h1>Test</h1></div>`)
				} else {
					writeTestResponse(t, w, mockPage(1, 1, ""))
				}
			})
			c.http.Jar.SetCookies(c.base, []*http.Cookie{{Name: "qrator_jsid2", Value: "old", Path: "/"}}) // #nosec G124 -- Synthetic clearance for the test's plain HTTP client jar.
			c.refreshCookies = func(_ context.Context, rejected []*http.Cookie) ([]*http.Cookie, error) {
				refreshed++
				if !sameClearance(rejected, []*http.Cookie{{Name: "qrator_jsid2", Value: "old"}}) { // #nosec G124 -- Synthetic rejected cookie for a local comparison.
					t.Fatal("did not identify the rejected clearance")
				}
				return []*http.Cookie{{Name: "qrator_jsid2", Value: "fresh", Path: "/"}, {Name: "city_code", Value: "perm", Path: "/"}}, nil // #nosec G124 -- Synthetic cookies for the test's plain HTTP client jar.
			}
			var err error
			if strings.Contains(challengePath, "/catalog/") {
				_, err = c.Detail(t.Context(), "1", "kazan", c.base.String()+challengePath)
			} else {
				_, err = c.Search(t.Context(), "кора осины", "kazan", 1)
			}
			if err != nil || refreshed != 1 || requests != 3 || c.requests != 3 {
				t.Fatalf("unexpected recovery: refresh=%d requests=%d err=%v", refreshed, requests, err)
			}
		})
	}
}

func TestPersistentChallengeStopsAfterOneRefresh(t *testing.T) {
	t.Parallel()
	refreshes := 0
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeTestResponse(t, w, `<script src="/__qrator/ldr.js"></script>`)
	})
	c.refreshCookies = func(context.Context, []*http.Cookie) ([]*http.Cookie, error) {
		refreshes++
		return []*http.Cookie{{Name: "qrator_jsid2", Value: "still-rejected", Path: "/"}}, nil // #nosec G124 -- Synthetic clearance for the test's plain HTTP client jar.
	}
	_, err := c.Search(t.Context(), "test", "kazan", 1)
	if !errors.Is(err, errBrowserCheck) || refreshes != 1 || c.requests != 2 {
		t.Fatalf("retried persistent challenge: refreshes=%d requests=%d err=%v", refreshes, c.requests, err)
	}
}

func TestResponseCookiesPersistOnlyAnonymousRenewals(t *testing.T) {
	t.Parallel()
	importer := testAuthImporter(t, func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{browserCookie("old", time.Now().Add(time.Minute))}}, nil
	})
	store, _, err := importer.importCookies(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	session := &authSession{importer: importer, store: store}
	past := time.Now().Add(-time.Hour)
	if err := session.saveCookies([]*http.Cookie{
		{Name: "qrator_jsid2", Value: "renewed", Path: "/", Expires: past, MaxAge: 3600}, // #nosec G124 -- Synthetic response cookies exercise persistence and filtering.
		{Name: "PHPSESSID", Value: "private-account", Path: "/"},                         // #nosec G124 -- Verify that account cookies are excluded regardless of flags.
		{Name: "qrator_jsid", Value: "other-host", Domain: "other.example", Path: "/"},   // #nosec G124 -- Verify that unrelated domains are excluded regardless of flags.
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := loadAuth(importer.path)
	if err != nil || len(updated.Cookies) != 1 || updated.Cookies[0].Value != "renewed" || !updated.Cookies[0].Expires.After(time.Now().Add(59*time.Minute)) {
		t.Fatalf("renewal was lost or private cookies persisted: %v", err)
	}
	if err := session.saveCookies([]*http.Cookie{{Name: "qrator_jsid2", Path: "/", MaxAge: -1}}); err != nil { // #nosec G124 -- Synthetic deletion instruction for the local cookie store.
		t.Fatal(err)
	}
	updated, err = loadAuth(importer.path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.hasClearance(time.Now()) {
		t.Fatal("cookie deletion was not persisted")
	}
	updated.ImportedAt = updated.ImportedAt.Add(time.Hour)
	if err := atomicJSON(importer.path, updated); err != nil {
		t.Fatal(err)
	}
	if err := session.saveCookies([]*http.Cookie{{Name: "qrator_jsid2", Value: "stale-command", Path: "/"}}); err != nil { // #nosec G124 -- Synthetic renewal from an older local command.
		t.Fatal(err)
	}
	updated, err = loadAuth(importer.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Cookies) != 0 {
		t.Fatal("an older command overwrote a newer import")
	}
}

func TestImportDoesNotCombineClearanceFromDifferentProfiles(t *testing.T) {
	t.Parallel()
	importer := testAuthImporter(t, func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		primary := browserCookie("primary", time.Now().Add(time.Hour))
		other := browserCookie("other-profile", time.Now().Add(time.Hour))
		other.Source.StorePath, other.Name = "/browser/Profile 1/Cookies", "qrator_jsid"
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{primary, other}}, nil
	})
	store, _, err := importer.importCookies(t.Context(), nil)
	if err != nil || len(store.Cookies) != 1 || store.Cookies[0].Value != "primary" {
		t.Fatalf("clearance from unrelated profiles was mixed: %v", err)
	}
}

func TestInvalidAuthWaitFlagsFailWithoutBrowserAccess(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"auth", "import", "--browser", "chrome", "--wait", "-1s"}, {"search", "test", "--auth-wait", "-1s"}, {"search", "test", "--page", "0"}} {
		var output bytes.Buffer
		if err := run(t.Context(), args, &output, &output); err == nil {
			t.Fatalf("invalid flags accepted: %v", args)
		}
	}
	var output bytes.Buffer
	if defaultAuthWait(&output) != 0 {
		t.Fatal("noninteractive output implicitly enabled waiting")
	}
}
