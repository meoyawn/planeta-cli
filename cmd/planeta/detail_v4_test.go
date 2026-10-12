package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRecordedV4DetailUsesTwoRequests(t *testing.T) {
	t.Parallel()
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			t.Parallel()
			body := fixture(t, "tilorone-detail-v4")
			path := "/test-city/catalog/lekarstva-i-bad/prostuda-i-gripp/protivovirusnye-immunokorrektory/tiloron-tab-125-mg-6-sht-48183503/"
			var requests atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if selectMockCity(t, w, r) {
					return
				}
				if r.URL.Path != path {
					t.Errorf("unexpected asset or instruction request: %s", r.URL)
				}
				writeTestResponse(t, w, string(body))
			})
			result, err := c.Detail(t.Context(), "48183503", "test-city", c.base.String()+path)
			if err != nil {
				t.Fatal(err)
			}
			if !full {
				result.concise()
			}
			if result.Name != "Тилорон таб 125 мг 6 шт" || result.PriceFrom == nil || *result.PriceFrom != 329 || result.Currency != "RUB" {
				t.Fatalf("missing identity or price: %+v", result)
			}
			if result.Manufacturer != "Макиз-Фарма, Россия" || result.ActiveIngredient != "тилорон" || result.Age != "18 лет" || result.Dosage != "125 мг" {
				t.Fatalf("missing product specifications: %+v", result)
			}
			if result.Package == nil || result.Package.Quantity != 6 || result.PackageUnitPrice == nil || result.PackageUnitPrice.Amount != 54.83 {
				t.Fatalf("missing package facts: %+v", result)
			}
			if !strings.Contains(result.ActiveSubstances, "тилорона дигидрохлорид – 125,00 мг") || !strings.Contains(result.Excipients, "краситель хинолиновый желтый алюминиевый лак") || strings.Contains(result.Ingredients, "гравировкой") {
				t.Fatalf("composition missing or mixed with description: %s", result.Ingredients)
			}
			assertPharmacyCount(t, result.PharmacyCounts.InStock, 6)
			assertPharmacyCount(t, result.PharmacyCounts.Orderable, 92)
			if result.ImageURL == "" || result.Prescription == "" || len(result.Warnings) != 0 || result.Expiry != "" || len(result.Labels) != 0 {
				t.Fatalf("missing source facts or unexpected warnings: %+v", result)
			}
			if len(result.InstructionLinks) != 2 || result.InstructionLinks[0].URL != c.base.String()+path+"pdf-instruction/" {
				t.Fatalf("instruction source links missing: %+v", result.InstructionLinks)
			}
			if full {
				if len(result.Instructions) != 15 || !strings.Contains(result.Directions, "На курс – 750 мг") {
					t.Fatalf("incomplete instructions: count=%d directions=%q", len(result.Instructions), result.Directions)
				}
				found := false
				for _, item := range result.Instructions {
					if item.Title == "Противопоказания" && strings.Contains(item.Text, "Беременность") {
						found = true
					}
				}
				if !found {
					t.Fatal("published contraindications missing")
				}
			} else if result.Instructions != nil || result.Directions != "" || result.SourceSchema != nil {
				t.Fatal("full instruction sources leaked into concise output")
			}
			if requests.Load() != 2 || result.HTTPRequests != 2 {
				t.Fatalf("request count: server=%d JSON=%d", requests.Load(), result.HTTPRequests)
			}
		})
	}
}
