package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

const siteOrigin = "https://planetazdorovo.ru"
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
const maxPageBytes = 16 << 20
const requestBudget = 2

var errBrowserCheck = errors.New("site verification needs a fresh Planeta cookie; reload https://planetazdorovo.ru/ in your existing browser, wait until the catalog appears, then run planeta auth import --browser chrome --wait 2m; no need to sign out")
var citySlug = regexp.MustCompile("^[a-z0-9]+(?:-[a-z0-9]+)*$")
var validID = regexp.MustCompile("^-?[1-9][0-9]*$")

type client struct {
	http           *http.Client
	base           *url.URL
	userAgent      string
	requests       int
	refreshCookies func(context.Context, []*http.Cookie) ([]*http.Cookie, error)
	saveCookies    func([]*http.Cookie) error
	refreshed      bool
}

// Each client belongs to one invocation: city selection and one data page.
// Redirects, automatic pagination, and ordinary retries are disabled. A browser
// cookie refresh may retry the challenged GET once, with one extra request.
func newClient(origin, userAgent string, cookies []*http.Cookie) (*client, error) {
	base, err := url.Parse(origin)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil {
		return nil, fmt.Errorf("invalid origin %q: expected an HTTP(S) origin", origin)
	}
	if userAgent == "" || strings.ContainsAny(userAgent, "\r\n") {
		return nil, fmt.Errorf("user-agent must be nonempty and contain no newlines")
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	jar.SetCookies(base, cookies)
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport has type %T; expected *http.Transport", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	// Prevent net/http's transparent retry on a failed reused connection.
	transport.DisableKeepAlives = true
	transport.ForceAttemptHTTP2 = false
	return &client{
		base: base, userAgent: userAgent,
		http: &http.Client{
			Transport: transport, Jar: jar, Timeout: 45 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func isAnonymousCookie(name string) bool {
	switch name {
	case "qrator_jsid", "qrator_jsid2", "city_id", "city_code", "region_id":
		return true
	}
	return false
}

// Optional legacy header-file support. Normal use is auth import.
func readCookies(path string) (result []*http.Cookie, err error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) // #nosec G304 G703 -- The CLI explicitly accepts a user-selected local cookie file.
	if err != nil {
		return nil, fmt.Errorf("open cookie file %q: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close cookie file %q: %w", path, closeErr))
		}
	}()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return nil, fmt.Errorf("read cookie file %q: %w", path, err)
	}
	if len(data) > 64<<10 {
		return nil, fmt.Errorf("cookie file %q exceeds 64 KiB", path)
	}
	header := strings.TrimSpace(string(data))
	if len(header) >= 7 && strings.EqualFold(header[:7], "Cookie:") {
		header = strings.TrimSpace(header[7:])
	}
	if header == "" || strings.ContainsAny(header, "\r\n") {
		return nil, fmt.Errorf("cookie file %q must contain one nonempty Cookie header line", path)
	}
	cookies, err := http.ParseCookie(header)
	if err != nil {
		return nil, fmt.Errorf("cookie file %q contains an invalid Cookie header", path)
	}
	var anonymous []*http.Cookie
	for _, cookie := range cookies {
		if isAnonymousCookie(cookie.Name) {
			cookie.Path = "/"
			anonymous = append(anonymous, cookie)
		}
	}
	if len(anonymous) == 0 {
		return nil, fmt.Errorf("cookie file %q contains no supported anonymous Qrator or city cookies", path)
	}
	return anonymous, nil
}

func (c *client) selectCity(ctx context.Context, city string) error {
	if !citySlug.MatchString(city) {
		return fmt.Errorf("invalid city %q: expected a lowercase city URL slug such as kazan", city)
	}
	u := c.base.ResolveReference(&url.URL{Path: "/" + city + "/"})
	if _, err := c.get(ctx, u); err != nil {
		return fmt.Errorf("select city %q: %w", city, err)
	}
	if got := c.cookieValue("city_code"); got != city {
		return fmt.Errorf("site selected city %q instead of requested city %q", got, city)
	}
	return nil
}

func (c *client) Search(ctx context.Context, query, city string, number int) (*searchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query %q must not be empty", query)
	}
	if number < 1 {
		return nil, fmt.Errorf("page %d must be at least 1", number)
	}
	if err := c.selectCity(ctx, city); err != nil {
		return nil, err
	}
	u := c.searchURL(query, number)
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("fetch search page %d: %w", number, err)
	}
	page, err := parseSearchPage(body, u, city)
	if err != nil {
		return nil, fmt.Errorf("parse search page %d: %w", number, err)
	}
	if number > page.totalPages {
		return nil, fmt.Errorf("page %d exceeds available pages (%d)", number, page.totalPages)
	}
	if page.currentPage != 0 && page.currentPage != number {
		return nil, fmt.Errorf("requested page %d but site returned page %d", number, page.currentPage)
	}
	result := &searchResult{
		Query: query, City: city, SourceURL: u.String(), FetchedAt: time.Now().UTC(), HTTPRequests: c.requests,
		Total: page.total, Count: len(page.products), Page: number, TotalPages: page.totalPages,
		HasMore: number < page.totalPages, Results: page.products,
	}
	if result.HasMore {
		next := number + 1
		result.NextPage, result.NextURL = &next, c.searchURL(query, next).String()
	}
	return result, nil
}

func (c *client) searchURL(query string, page int) *url.URL {
	values := url.Values{"q": {query}}
	if page > 1 {
		values.Set("PAGEN_1", strconv.Itoa(page))
	}
	return c.base.ResolveReference(&url.URL{Path: "/search/", RawQuery: values.Encode()})
}

func (c *client) Detail(ctx context.Context, id, city, sourceURL string) (*detailResult, error) {
	u, err := validateProductURL(sourceURL, c.base, city, id)
	if err != nil {
		return nil, err
	}
	if err := c.selectCity(ctx, city); err != nil {
		return nil, err
	}
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("fetch product %s: %w", id, err)
	}
	result, err := parseDetailPage(body, u, city, id)
	if err != nil {
		return nil, fmt.Errorf("parse product %s: %w", id, err)
	}
	result.HTTPRequests = c.requests
	result.FetchedAt = time.Now().UTC()
	return result, nil
}

func validateProductURL(raw string, base *url.URL, city, id string) (*url.URL, error) {
	if !validID.MatchString(id) {
		return nil, fmt.Errorf("invalid product id %q: expected a numeric catalog ID", id)
	}
	if !citySlug.MatchString(city) {
		return nil, fmt.Errorf("invalid city %q: expected a lowercase city URL slug such as kazan", city)
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme != base.Scheme || u.Host != base.Host || !strings.HasPrefix(u.Path, "/"+city+"/catalog/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("product URL must be a canonical %s/%s/catalog/ URL for id %s", base, city, id)
	}
	match := productID.FindStringSubmatch(u.Path)
	if len(match) != 2 || match[1] != id {
		return nil, fmt.Errorf("product URL does not match requested id %s", id)
	}
	return u, nil
}

func (c *client) cookieValue(name string) string {
	for _, cookie := range c.http.Jar.Cookies(c.base) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (c *client) get(ctx context.Context, target *url.URL) ([]byte, error) {
	rejected := c.http.Jar.Cookies(c.base)
	body, err := c.getOnce(ctx, target)
	if !errors.Is(err, errBrowserCheck) || c.refreshCookies == nil || c.refreshed {
		return body, err
	}
	c.refreshed = true
	cookies, err := c.refreshCookies(ctx, rejected)
	if err != nil {
		return nil, err
	}
	// Preserve the requested city when the challenge occurs on the data page.
	// Browser city preferences must not change a search halfway through.
	for _, cookie := range c.http.Jar.Cookies(c.base) {
		if isClearanceCookie(cookie.Name) {
			c.http.Jar.SetCookies(c.base, []*http.Cookie{{Name: cookie.Name, Path: "/", MaxAge: -1}}) // #nosec G124 -- Expire a local jar entry; this does not issue a server cookie.
		}
	}
	for _, cookie := range cookies {
		if isClearanceCookie(cookie.Name) {
			c.http.Jar.SetCookies(c.base, []*http.Cookie{cookie})
		}
	}
	return c.getOnce(ctx, target)
}

func (c *client) getOnce(ctx context.Context, target *url.URL) (body []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget := requestBudget
	if c.refreshed {
		budget++
	}
	if c.requests >= budget {
		return nil, fmt.Errorf("HTTP request budget of %d exhausted", budget)
	}
	if target.Scheme != c.base.Scheme || target.Host != c.base.Host {
		return nil, fmt.Errorf("refusing request outside the catalog origin")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil) // #nosec G704 -- Scheme and host match the configured catalog origin above.
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.5")
	c.requests++
	resp, err := c.http.Do(req) // #nosec G704 -- The origin is validated above and the client rejects redirects.
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close response: %w", closeErr))
		}
	}()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if strings.Contains(strings.ToLower(string(body)), "/__qrator/") {
		return nil, errBrowserCheck
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("HTTP %d redirect from %s; redirects are disabled to keep the request count fixed; repeat search to refresh product URLs", resp.StatusCode, target.Path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s (no automatic retries)", resp.StatusCode, target.Path)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read response: %w", readErr)
	}
	if len(body) > maxPageBytes {
		return nil, fmt.Errorf("page exceeds %d bytes", maxPageBytes)
	}
	if c.saveCookies != nil {
		if err := c.saveCookies(resp.Cookies()); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func pageNumber(u *url.URL) int {
	value := u.Query().Get("PAGEN_1")
	if value == "" {
		return 1
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return number
}
