package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name + ".html") // #nosec G304 -- Fixture names are constants supplied by these tests.
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestParseRecordedSearchMatchesChrome(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, "https://planetazdorovo.ru/search/?q=магний+хелат")
	page, err := parseSearchPage(fixture(t, "magnesium-chelate"), u, "test-city")
	if err != nil {
		t.Fatal(err)
	}
	// Independently recorded from the anonymous public catalog.
	want := []string{"15484411", "46932003", "42927303", "46969703", "8503811", "17553111", "23162311", "20976011", "20519511", "19637811", "5259511", "10647611", "49198503", "24463811", "23783011"}
	var got []string
	for _, p := range page.products {
		got = append(got, p.ID)
	}
	if page.total != 15 || !reflect.DeepEqual(got, want) {
		t.Fatalf("total=%d, IDs=%v; want 15, %v", page.total, got, want)
	}
	first := page.products[0]
	if first.Name != "Магний хелат таб 120 шт Эвалар" || first.PriceFrom == nil || *first.PriceFrom != 2163 || first.Currency != "RUB" {
		t.Fatalf("unexpected first product: %+v", first)
	}
	if first.Manufacturer != "Эвалар ЗАО, Россия" || len(first.Availability) != 2 || !strings.HasPrefix(first.ImageURL, "https://img.planetazdorovo.ru/") {
		t.Fatalf("missing product details: %+v", first)
	}
}

func TestNoMatchesDoesNotReturnRecommendations(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, "https://planetazdorovo.ru/search/?q=zzzxqv-no-such-product-927461")
	body := fixture(t, "no-matches")
	if !strings.Contains(string(body), `data-count_element="6"`) {
		t.Fatal("fixture must include the website's six recommendations")
	}
	page, err := parseSearchPage(body, u, "test-city")
	if err != nil {
		t.Fatal(err)
	}
	if page.total != 0 || len(page.products) != 0 || len(page.links) != 0 {
		t.Fatalf("recommendations leaked into search results: %+v", page)
	}
}

func TestNoMatchesWithQueryInHeading(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, "https://planetazdorovo.ru/search/?q=15484411")
	page, err := parseSearchPage([]byte(`<h1>По запросу "<b>15484411</b>" ничего не найдено</h1>`), u, "test-city")
	if err != nil || page.total != 0 || len(page.products) != 0 {
		t.Fatalf("zero-match heading rejected: page=%+v error=%v", page, err)
	}
}

func TestDiscountedBatchesWithoutSchemaMetadataAreIncluded(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, "https://planetazdorovo.ru/search/?q=магний&PAGEN_1=2")
	page, err := parseSearchPage(fixture(t, "discounted-batches"), u, "test-city")
	if err != nil {
		t.Fatal(err)
	}
	if page.total != 193 || len(page.products) != 36 {
		t.Fatalf("discounted products lost: total=%d products=%d", page.total, len(page.products))
	}
	want := []string{"-157028", "-154385", "-154924", "-154441", "-155668"}
	wantPharmacies := map[string]int{"-157028": 1, "-154385": 1, "-154924": 1, "-154441": 1, "-155668": 2}
	var got []string
	for _, item := range page.products {
		if strings.HasPrefix(item.ID, "-") {
			got = append(got, item.ID)
			assertPharmacyCount(t, item.PharmacyCounts.InStock, wantPharmacies[item.ID])
			if item.Name == "" || item.PriceFrom == nil || item.Currency != "RUB" || item.ImageURL == "" {
				t.Fatalf("discounted batch missing details: %+v", item)
			}
			if item.ID == "-157028" && *item.PriceFrom != 872 {
				t.Fatalf("wrong discounted price: %v", *item.PriceFrom)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discounted batch IDs: got %v want %v", got, want)
	}
}

func TestParserRejectsIncompleteOrChangedMarkup(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, "https://planetazdorovo.ru/search/?q=test")
	body := string(fixture(t, "magnesium-chelate"))
	cases := map[string]string{
		"missing heading":            "<html><body>Temporarily unavailable</body></html>",
		"missing cards":              strings.ReplaceAll(body, `data-name="Результаты_поиска"`, `data-name="changed"`),
		"different pagination total": strings.ReplaceAll(body, `data-count_element="15"`, `data-count_element="16"`),
		"wrong city":                 strings.ReplaceAll(body, "/test-city/catalog/", "/other-city/catalog/"),
		"bad price":                  strings.Replace(body, `content="2163.00"`, `content="NaN"`, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseSearchPage([]byte(input), u, "test-city"); err == nil {
				t.Fatal("expected an error instead of misleading results")
			}
		})
	}
}

func TestCountComesFromHeadingSuffix(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, "https://planetazdorovo.ru/search/?q=test")
	body := strings.Replace(string(fixture(t, "magnesium-chelate")), "<b>магний хелат</b>", "<b>найдено 999 товаров</b>", 1)
	page, err := parseSearchPage([]byte(body), u, "test-city")
	if err != nil || page.total != 15 {
		t.Fatalf("query text confused the result count: page=%+v err=%v", page, err)
	}
}
