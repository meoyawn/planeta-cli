package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steipete/sweetcookie"
)

func TestBrowserImportScopeFilteringAndSecretFreeJSON(t *testing.T) {
	t.Parallel()
	browser, err := NewBrowserFromValue("Chrome")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "planeta", "auth.json")
	expires := time.Now().Add(time.Hour)
	read := func(_ context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
		if opts.URL != siteOrigin+"/" || opts.AllowAllHosts || opts.IncludeExpired || len(opts.Origins) != 0 {
			t.Fatalf("browser import widened its scope: %+v", opts)
		}
		if !reflect.DeepEqual(opts.Browsers, []sweetcookie.Browser{sweetcookie.BrowserChrome}) || opts.Profiles[sweetcookie.BrowserChrome] != "Profile 1" {
			t.Fatalf("wrong browser/profile: %+v", opts)
		}
		if len(opts.Names) != 5 {
			t.Fatal("missing name allowlist")
		}
		for _, name := range opts.Names {
			if !isAnonymousCookie(name) {
				t.Fatal("account cookie requested")
			}
		}
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{
			{Name: "qrator_jsid2", Value: "secret-clearance", Domain: ".planetazdorovo.ru", Path: "/", Expires: &expires, Secure: true},
			{Name: "city_code", Value: "test-city", Domain: "planetazdorovo.ru", Path: "/"},
			{Name: "PHPSESSID", Value: "private-account", Domain: "planetazdorovo.ru", Path: "/"},
			{Name: "qrator_jsid2", Value: "other-site", Domain: "other.example", Path: "/"},
		}}, nil
	}
	result, err := importBrowserCookies(t.Context(), browser, "Profile 1", path, read)
	if err != nil {
		t.Fatal(err)
	}
	if result.CookieCount != 2 || result.HTTPRequests != 0 {
		t.Fatalf("bad metadata: %+v", result)
	}
	data := marshalTestJSON(t, result)
	if bytes.Contains(data, []byte("secret-clearance")) || bytes.Contains(data, []byte("private-account")) {
		t.Fatal("secret in auth JSON")
	}
	stored, err := loadAuth(path)
	if err != nil || len(stored.httpCookies()) != 2 {
		t.Fatalf("stored cookies missing: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe store permissions: %v", err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || parent.Mode().Perm() != 0700 {
		t.Fatalf("unsafe directory permissions: %v", err)
	}
	loaded, err := loadClientCookies("", path, t.TempDir())
	if err != nil || len(loaded) != 2 {
		t.Fatalf("global import not loaded: %v", err)
	}
}

func TestFailedImportPreservesPreviousStore(t *testing.T) {
	t.Parallel()
	browser, err := NewBrowserFromValue("chrome")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(-time.Hour)
	_, err = importBrowserCookies(t.Context(), browser, "", path, func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{
			{Name: "qrator_jsid2", Value: "expired", Domain: "planetazdorovo.ru", Path: "/", Expires: &expires},
			{Name: "city_code", Value: "test-city", Domain: "planetazdorovo.ru", Path: "/"},
		}, Warnings: []string{"test keychain warning"}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "test keychain warning") {
		t.Fatalf("expected actionable error: %v", err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- Path points to this test's temporary auth store.
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "previous" {
		t.Fatal("failed import destroyed previous cookies")
	}
	if _, err := NewBrowserFromValue("not-a-browser"); err == nil {
		t.Fatal("accepted invalid browser")
	}
}

func TestProductURLIndexPersistsAcrossSearchesAndCities(t *testing.T) {
	t.Parallel()
	p := appPaths{index: filepath.Join(t.TempDir(), "products")}
	for _, test := range []struct{ city, id string }{{"test-city", "1"}, {"test-city", "2"}, {"other-city", "1"}, {"test-city", "-157028"}} {
		item := product{ID: test.id, URL: siteOrigin + "/" + test.city + "/catalog/test-" + test.id + "/"}
		if err := p.remember(test.city, []product{item}); err != nil {
			t.Fatal(err)
		}
	}
	for _, city := range []string{"test-city", "other-city"} {
		got, err := p.lookup(city, "1")
		if err != nil || !strings.Contains(got, "/"+city+"/") {
			t.Fatalf("lookup %q: %q %v", city, got, err)
		}
	}
	if _, err := p.lookup("test-city", "3"); err == nil || !strings.Contains(err.Error(), "search first") {
		t.Fatalf("unknown ID not actionable: %v", err)
	}
	if _, err := p.lookup("../unsafe", "1"); err == nil {
		t.Fatal("accepted unsafe city")
	}
	if err := p.remember("test-city", []product{{ID: "1", URL: "https://other.example/test-city/catalog/test-1/"}}); err == nil {
		t.Fatal("saved external URL")
	}
}

func TestRecordedDetailAndConciseOutput(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, siteOrigin+"/test-city/catalog/test-15484411/")
	result, err := parseDetailPage(fixture(t, "magnesium-detail"), u, "test-city", "15484411")
	if err != nil {
		t.Fatal(err)
	}
	if result.PriceFrom == nil || *result.PriceFrom != 2163 || result.Brand != "Хелаты Эвалар" || result.PackageQuantity != "120 шт" {
		t.Fatalf("missing product facts: %+v", result)
	}
	if len(result.Instructions) != 9 || result.Directions == "" || len(result.SourceSchema) == 0 {
		t.Fatal("full instructions missing")
	}
	if !strings.Contains(result.Ingredients, "Магний 200 мг") || !strings.Contains(result.Ingredients, "Сорбит") || strings.Contains(result.Ingredients, "Основная функция") {
		t.Fatalf("ingredients missing or marketing included: %s", result.Ingredients)
	}
	if !strings.Contains(result.Excipients, "Сорбит") || !strings.Contains(result.Excipients, "полиэтиленгликоль") {
		t.Fatalf("excipients hidden: %s", result.Excipients)
	}
	if result.Package == nil || result.Package.Quantity != 120 || result.PackageUnitPrice == nil || result.PackageUnitPrice.Amount != 18.03 {
		t.Fatalf("missing package and unit pricing: package=%+v price=%+v", result.Package, result.PackageUnitPrice)
	}
	result.concise()
	var out bytes.Buffer
	if err := writeJSON(&out, result); err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(out.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"directions", "instructions", "source_schema", "source_text", "unit_price", "elemental_magnesium"} {
		if _, ok := values[key]; ok {
			t.Errorf("%s leaked into default id output", key)
		}
	}
	for _, key := range []string{"ingredients", "active_substances", "excipients", "composition_amounts", "dosage", "price_from", "manufacturer", "brand", "package_quantity", "package", "package_unit_price"} {
		if _, ok := values[key]; !ok {
			t.Errorf("%s missing from default id output", key)
		}
	}
}

func TestDefaultIDPreservesEntireExcipientListAndSorbitolWarning(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, siteOrigin+"/test-city/catalog/test-1/")
	excipients := "Карбонат кальция, гидроксипропилметилцеллюлоза, твин 80 (эмульгатор), полиэтиленгликоль, тальк; сорбит, мальтодекстрин; целлюлоза микрокристаллическая, кроскарамеллоза; стеарат кальция."
	note := "Содержит подсластитель сорбит, который при чрезмерном употреблении может оказывать слабительное действие."
	body := []byte(`<div class="product-detail" data-id="1"><h1>Магний 60 шт</h1></div>
<div class="product-detail-description-content__item" id="instruction_COMPOSITION"><h3>Состав</h3><div class="product-detail-description-content__item-content"><b>Активное вещество:</b><br>Магния бисглицинат.<br><br>Содержание активных веществ в 1 таблетке:<br>Магний 200 мг<br><b>Вспомогательные вещества:</b><br>` + excipients + `<br><br>` + note + `<br><b>Описание:</b><br>Marketing copy.</div></div>
<div class="product-detail-description-content__item" id="instruction_USEMETHODANDDOSES"><h3>Дозы</h3><div class="product-detail-description-content__item-content">Take with meals.</div></div>`)
	result, err := parseDetailPage(body, u, "test-city", "1")
	if err != nil {
		t.Fatal(err)
	}
	result.concise()
	if result.Excipients != excipients+"\n"+note {
		t.Fatalf("excipient list/warning changed: %q", result.Excipients)
	}
	data := marshalTestJSON(t, result)
	if bytes.Contains(data, []byte("Take with meals")) || bytes.Contains(data, []byte("Marketing copy")) {
		t.Fatal("directions or marketing leaked into default facts")
	}
}

func TestPackageFactsUseExplicitQuantityAndDoNotMistakeStrengthForSize(t *testing.T) {
	t.Parallel()
	price := 504.0
	for _, test := range []struct {
		name, quantity, source, unit string
		count, unitAmount            float64
	}{
		{"Магний 30 шт", "", "name", "шт", 30, 16.8},
		{"Магний 200 мг", "", "", "", 0, 0},
		{"Магний 30 шт + подарок 10 шт", "", "", "", 0, 0},
		{"Сироп", "150 мл", "specifications", "мл", 150, 3.36},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := detailResult{product: product{Name: test.name, PriceFrom: &price, Currency: "RUB"}, PackageQuantity: test.quantity}
			result.addPackageFacts()
			if test.count == 0 {
				if result.Package != nil || result.PackageUnitPrice != nil {
					t.Fatal("guessed a package size")
				}
				return
			}
			if result.Package == nil || result.Package.Quantity != test.count || result.Package.Unit != test.unit || result.Package.Source != test.source || result.PackageUnitPrice == nil || result.PackageUnitPrice.Amount != test.unitAmount {
				t.Fatalf("wrong package facts: package=%+v price=%+v", result.Package, result.PackageUnitPrice)
			}
		})
	}
}

func TestInstructionTablesRetainCellBoundaries(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, siteOrigin+"/test-city/catalog/test-1/")
	body := []byte(`<div class="product-detail" data-id="1"><h1>Test</h1></div>
<div class="product-detail-description-content__item" id="instruction_COMPOSITION"><h3>Состав</h3><div class="product-detail-description-content__item-content">
<table><tr><th>Вещество</th><th>В 2 капсулах</th></tr><tr><td>Магний</td><td>200 мг</td></tr><tr><td>B6</td><td>2 мг</td></tr></table></div></div>`)
	result, err := parseDetailPage(body, u, "test-city", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instructions) != 1 || len(result.Instructions[0].Tables) != 1 || result.Instructions[0].Tables[0][1][1] != "200 мг" {
		t.Fatalf("table data lost: %+v", result.Instructions)
	}
	if !strings.Contains(result.Ingredients, "Магний | 200 мг") {
		t.Fatalf("table cell boundaries lost: %s", result.Ingredients)
	}
	result.concise()
	if len(result.CompositionTables) != 1 || len(result.CompositionAmounts) != 2 || result.CompositionAmounts[0].Per == nil || result.CompositionAmounts[0].Per.Quantity != 2 {
		t.Fatalf("default output lost composition amounts or table serving: %+v", result.CompositionAmounts)
	}
}

func TestMissingCompositionIsReportedEvenWithGenericDisclaimer(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, siteOrigin+"/test-city/catalog/test-1/")
	body := []byte(`<div class="product-detail" data-id="1"><h1>Test</h1></div>
<div class="product-detail-description-content__item" id="instruction_description"><h3>Информация</h3><div class="product-detail-description-content__item-content">Generic site disclaimer.</div></div>`)
	result, err := parseDetailPage(body, u, "test-city", "1")
	if err != nil {
		t.Fatal(err)
	}
	result.concise()
	if result.Ingredients != "" || len(result.Warnings) == 0 || result.Instructions != nil {
		t.Fatalf("missing label not reported: %+v", result)
	}
}

func TestDosageFallbackPreservesExplicitServingSize(t *testing.T) {
	t.Parallel()
	input := "Активное вещество:\nМагния бисглицинат.\nСодержание активных веществ в 1 таблетке:\nМагний 200 мг\nВитамин B6 2 мг\nСодержание активных веществ в 4 таблетках:\nМагний 800 мг\nВспомогательные вещества:\nМКЦ"
	want := "Содержание активных веществ в 1 таблетке:\nМагний 200 мг\nВитамин B6 2 мг"
	if got := declaredServingContents(input); got != want {
		t.Fatalf("dosage serving lost or extra text included: %q", got)
	}
	if got := declaredServingContents("Магний хелат 500 мг"); got != "" {
		t.Fatalf("inferred a serving amount from ambiguous text: %q", got)
	}
}

func TestCLIFlagsAndValidationWithoutBrowserOrNetwork(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--help"}, {"id", "--help"}, {"auth", "import", "--help"}} {
		var out, stderr bytes.Buffer
		if err := run(t.Context(), args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
	}
	var out, stderr bytes.Buffer
	for _, args := range [][]string{{"auth", "import", "--browser", "invalid"}, {"id", "abc"}, {"search", "--timeout", "-1s", "test"}} {
		if err := run(t.Context(), args, &out, &stderr); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	browser, err := NewBrowserFromValue("chrome")
	if err != nil {
		t.Fatal(err)
	}
	_, err = importBrowserCookies(ctx, browser, "", filepath.Join(t.TempDir(), "auth.json"), func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
}
