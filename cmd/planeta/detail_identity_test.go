package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// These URL/SKU pairs are taken from the public Kazan product pages. The site
// keeps an older ID in the canonical URL after changing the internal SKU.
func TestDetailAcceptsVerifiedCatalogAliases(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ id, sku, slug, name string }{
		{"5584", "30278703", "ibuprofen-gel-dlya", "Ибупрофен гель 5% 50 г"},
		{"31278", "2981411", "nurofen-ekspress-gel", "Нурофен Экспресс гель для наружного применения 50 г от боли"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			u := parseTestURL(t, siteOrigin+"/kazan/catalog/"+tc.slug+"-"+tc.id+"/")
			body := aliasDetailHTML(t, tc.sku, u.String(), tc.sku, u.String(), tc.name)
			result, err := parseDetailPage([]byte(body), u, "kazan", tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if result.ID != tc.id || result.URL != u.String() || result.CatalogID != tc.sku {
				t.Fatalf("lost URL identity or catalog SKU: %+v", result)
			}
			if result.Name != tc.name || result.ActiveIngredient != "ибупрофен" || result.Prescription == "" {
				t.Fatalf("alias schema facts missing: %+v", result)
			}
			var schema map[string]any
			if err := json.Unmarshal(result.SourceSchema, &schema); err != nil || schema["sku"] != tc.sku {
				t.Fatalf("wrong product schema: %s, %v", result.SourceSchema, err)
			}
			result.concise()
			if result.CatalogID != tc.sku || result.SourceSchema != nil {
				t.Fatal("concise output lost catalog identity or retained full schema")
			}
			paths := appPaths{index: filepath.Join(t.TempDir(), "products")}
			if err := paths.remember("kazan", []product{result.product}); err != nil {
				t.Fatal(err)
			}
			if saved, err := paths.lookup("kazan", tc.id); err != nil || saved != u.String() {
				t.Fatalf("public URL ID was not preserved: URL=%q error=%v", saved, err)
			}
			if _, err := paths.lookup("kazan", tc.sku); err == nil {
				t.Fatal("internal SKU incorrectly stored as a URL ID")
			}
		})
	}
}

func TestDetailRejectsUnverifiedCatalogAliases(t *testing.T) {
	t.Parallel()
	u := parseTestURL(t, siteOrigin+"/kazan/catalog/ibuprofen-gel-dlya-5584/")
	other := siteOrigin + "/kazan/catalog/other-30278703/"
	for _, tc := range []struct{ name, pageID, canonical, sku, schemaURL string }{
		{"different product", "30278703", other, "30278703", other},
		{"missing canonical", "30278703", "", "30278703", u.String()},
		{"different canonical", "30278703", other, "30278703", u.String()},
		{"different schema URL", "30278703", u.String(), "30278703", other},
		{"missing schema URL", "30278703", u.String(), "30278703", ""},
		{"different schema SKU", "30278703", u.String(), "999", u.String()},
		{"old SKU conflicts with page", "30278703", u.String(), "5584", u.String()},
		{"invalid catalog SKU", "invalid", u.String(), "invalid", u.String()},
		{"canonical alone is insufficient", "", u.String(), "30278703", u.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := aliasDetailHTML(t, tc.pageID, tc.canonical, tc.sku, tc.schemaURL, "Other product")
			_, err := parseDetailPage([]byte(body), u, "kazan", "5584")
			if err == nil || !strings.Contains(err.Error(), "5584") {
				t.Fatalf("unverified identity accepted or error missing requested ID: %v", err)
			}
		})
	}
}

func TestDetailAliasUsesTwoRequests(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if selectMockCity(t, w, r) {
			return
		}
		if r.URL.Path != "/kazan/catalog/ibuprofen-gel-dlya-5584/" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		// Relative canonical/schema URLs resolve against the requested page.
		writeTestResponse(t, w, aliasDetailHTML(t, "30278703", r.URL.Path, "30278703", r.URL.Path, "Ибупрофен"))
	})
	u := c.base.String() + "/kazan/catalog/ibuprofen-gel-dlya-5584/"
	result, err := c.Detail(t.Context(), "5584", "kazan", u)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || result.HTTPRequests != 2 || result.CatalogID != "30278703" {
		t.Fatalf("unexpected request count or identity: %+v", result)
	}
}

func aliasDetailHTML(t *testing.T, pageID, canonical, sku, schemaURL, name string) string {
	t.Helper()
	schema := marshalTestJSON(t, map[string]any{
		"@context": "https://schema.org", "@type": []string{"Drug", "Product"},
		"sku": sku, "url": schemaURL, "name": name,
		"activeIngredient": "ибупрофен", "dosageForm": "гель для наружного применения",
		"prescriptionStatus": "https://schema.org/OTC",
	})
	return fmt.Sprintf(`<link rel="canonical" href="%s">
<div id="catalog-element-data" data-id="%s"><div class="product-detail card">
<div class="product-detail__wrap"><h1>%s в Казани</h1><div class="product-detail__price">87 ₽</div></div>
</div></div><script type="application/ld+json">%s</script>`, canonical, pageID, name, schema)
}
