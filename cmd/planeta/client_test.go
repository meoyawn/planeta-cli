package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func mockPage(total int, id int, links string) string {
	return fmt.Sprintf(`<h1>По запросу "магний &amp; B6" найдено %d товаров</h1>
<div data-name="Результаты_поиска"><div class="item-card">
<a class="item-card-title-text" href="/kazan/catalog/test-%d/"><span itemprop="name">Товар %d</span></a>
<meta itemprop="priceCurrency" content="RUB"><div itemprop="price" content="250.50"></div>
<p class="item-card-availability-text">Забрать сегодня: 1 аптека</p>
<p class="item-card-availability-text">Завтра и позже: 2 аптеки</p>
</div></div><div class="pagination" data-count_element="%d">%s</div>`, total, id, id, total, links)
}

func testClient(t *testing.T, handler http.HandlerFunc) *client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newClient(server.URL, defaultUserAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.http.CloseIdleConnections)
	return c
}

func selectMockCity(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/kazan/" {
		http.SetCookie(w, &http.Cookie{Name: "city_code", Value: "kazan", Path: "/"})
		fmt.Fprint(w, "<h1>Казань</h1>")
		return true
	}
	return false
}

func TestSearchFetchesOnlyRequestedPageWithTwoRequests(t *testing.T) {
	for _, number := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			var requests atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if selectMockCity(w, r) {
					return
				}
				if r.URL.Query().Get("q") != "магний & B6" || pageNumber(r.URL) != number {
					t.Errorf("wrong page/query: %s", r.URL)
				}
				cookie, err := r.Cookie("city_code")
				if err != nil || cookie.Value != "kazan" {
					t.Error("anonymous city cookie missing")
				}
				links := `<a href="?PAGEN_1=3&amp;q=магний+%26+B6">3</a><a href="?PAGEN_1=2&amp;q=магний+%26+B6">2</a><a href="?q=магний+%26+B6">1</a>`
				fmt.Fprint(w, mockPage(3, 100+number, links))
			})
			result, err := c.Search(context.Background(), "магний & B6", "kazan", number)
			if err != nil {
				t.Fatal(err)
			}
			if result.Total != 3 || result.Count != 1 || result.Page != number || result.TotalPages != 3 || result.HasMore != (number < 3) {
				t.Fatalf("wrong page metadata: %+v", result)
			}
			if result.Results[0].ID != fmt.Sprint(100+number) {
				t.Fatalf("wrong product: %+v", result.Results)
			}
			assertPharmacyCount(t, result.Results[0].PharmacyCounts.InStock, 1)
			assertPharmacyCount(t, result.Results[0].PharmacyCounts.Orderable, 2)
			if requests.Load() != 2 || result.HTTPRequests != 2 {
				t.Fatalf("request count: server=%d JSON=%d", requests.Load(), result.HTTPRequests)
			}
			if number < 3 && (result.NextPage == nil || *result.NextPage != number+1) {
				t.Fatal("missing next page")
			}
			// Even an accidental later call cannot spend another request.
			if _, err := c.get(context.Background(), c.base); err == nil || requests.Load() != 2 {
				t.Fatal("request budget not enforced")
			}
		})
	}
}

func TestDetailAndFullUseTwoRequestsAndDoNotFetchAssets(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			var requests atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if selectMockCity(w, r) {
					return
				}
				if r.URL.Path != "/kazan/catalog/test-15484411/" {
					t.Errorf("unexpected asset/follow-up request: %s", r.URL)
				}
				fmt.Fprint(w, `<div data-id="15484411"><div class="product-detail">
<div class="product-detail__wrap"><h1>Магний</h1><div class="product-detail__price">200 ₽</div>
<img class="product-detail__img" src="/never-fetch.jpg"></div>
<div class="product-detail__availability"><p>В наличии в 1 аптеке</p><p>Под заказ в 3 аптеках</p></div></div></div>
<div class="product-detail-description-content__item" id="instruction_COMPOSITION"><h3>Состав</h3><div class="product-detail-description-content__item-content">Магний 100 мг</div></div>
<div class="product-detail-description-content__item" id="instruction_USEMETHODANDDOSES"><h3>Способ применения и дозы</h3><div class="product-detail-description-content__item-content">С едой. <a href="/never-fetch.pdf">Инструкция</a></div></div>`)
			})
			result, err := c.Detail(context.Background(), "15484411", "kazan", c.base.String()+"/kazan/catalog/test-15484411/")
			if err != nil {
				t.Fatal(err)
			}
			if !full {
				result.concise()
			}
			if requests.Load() != 2 || result.HTTPRequests != 2 {
				t.Fatalf("requests=%d", requests.Load())
			}
			assertPharmacyCount(t, result.PharmacyCounts.InStock, 1)
			assertPharmacyCount(t, result.PharmacyCounts.Orderable, 3)
			if full && (len(result.Instructions) != 2 || result.Directions == "") {
				t.Fatal("full instructions missing")
			}
			if !full && (result.Instructions != nil || result.Directions != "") {
				t.Fatal("instructions or directions leaked into concise result")
			}
		})
	}
}

func TestFailuresDoNotRetryOrFollowRedirects(t *testing.T) {
	for _, status := range []int{401, 302, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "/never-fetch")
				w.WriteHeader(status)
				if status == 401 {
					fmt.Fprint(w, `<script src="/__qrator/ldr.js"></script>`)
				}
			})
			_, err := c.Search(context.Background(), "магний хелат", "kazan", 1)
			if err == nil || requests.Load() != 1 {
				t.Fatalf("error=%v requests=%d", err, requests.Load())
			}
			if status == 401 && !errors.Is(err, errBrowserCheck) {
				t.Fatalf("expected actionable browser check error: %v", err)
			}
		})
	}
}

func TestWrongCityAndCancellation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "city_code", Value: "perm", Path: "/"})
		fmt.Fprint(w, "<h1>Пермь</h1>")
	})
	_, err := c.Search(context.Background(), "test", "kazan", 1)
	if err == nil || !strings.Contains(err.Error(), `"perm"`) || c.requests != 1 {
		t.Fatalf("wrong city accepted: %v", err)
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled search sent a request") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Search(ctx, "test", "kazan", 1); !errors.Is(err, context.Canceled) || c.requests != 0 {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestInvalidURLsAndInputMakeNoRequests(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input sent HTTP") })
	for _, raw := range []string{
		"https://other.example/kazan/catalog/test-1/",
		c.base.String() + "/moskva/catalog/test-1/",
		c.base.String() + "/kazan/catalog/test-2/",
		c.base.String() + "/kazan/catalog/test-1/?q=extra",
	} {
		if _, err := c.Detail(context.Background(), "1", "kazan", raw); err == nil {
			t.Errorf("accepted URL %q", raw)
		}
	}
	if _, err := c.Search(context.Background(), "test", "kazan", 0); err == nil {
		t.Fatal("accepted page 0")
	}
	if _, err := c.Search(context.Background(), "", "kazan", 1); err == nil {
		t.Fatal("accepted empty query")
	}
	base, _ := url.Parse(siteOrigin)
	if _, err := validateProductURL(siteOrigin+"/kazan/catalog/test--157028/", base, "kazan", "-157028"); err != nil {
		t.Fatalf("negative batch id rejected: %v", err)
	}
}

func TestCookieFileDoesNotImportAccountCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies")
	if err := os.WriteFile(path, []byte("Cookie: qrator_jsid2=clearance; city_code=kazan; PHPSESSID=private; BITRIX_SM_LOGIN=private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cookies, err := readCookies(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 2 || cookies[0].Name != "qrator_jsid2" || cookies[1].Name != "city_code" {
		t.Fatalf("unexpected imported cookie count: %d", len(cookies))
	}
	if err := os.WriteFile(path, []byte("qrator_jsid2=private\nInjected: private"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCookies(path); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("invalid cookie input was accepted or exposed: %v", err)
	}
}
