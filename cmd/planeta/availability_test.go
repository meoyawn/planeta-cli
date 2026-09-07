package main

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestPharmacyCountsNormalizeSearchAndDetailLabels(t *testing.T) {
	for _, test := range []struct {
		search, detail []string
		stock, order   int
	}{
		{[]string{"Забрать сегодня: 1 аптека"}, []string{"В наличии в 1 аптеке"}, 1, -1},
		{[]string{"Забрать сегодня: 20 аптек", "Завтра и позже: 93 аптеки"}, []string{"В наличии в 20 аптеках", "Под заказ в 93 аптеках"}, 20, 93},
		{[]string{"Завтра и позже: 2 аптеки"}, []string{"Под заказ в 2 аптеках"}, -1, 2},
		{[]string{"Забрать сегодня: 0 аптек"}, []string{"В наличии в 0 аптеках"}, 0, -1},
		{[]string{"Забрать сегодня: 1\u00a0201 аптека"}, []string{"В наличии в 1 201 аптеке"}, 1201, -1},
	} {
		search := parsePharmacyCounts(test.search)
		detail := parsePharmacyCounts(test.detail)
		if !reflect.DeepEqual(search, detail) {
			t.Fatalf("search/detail counts differ: %+v %+v", search, detail)
		}
		assertPharmacyCount(t, search.InStock, test.stock)
		assertPharmacyCount(t, search.Orderable, test.order)
	}
}

func TestPharmacyCountsKeepUnknownDistinctFromZeroAndDoNotAddOverlappingGroups(t *testing.T) {
	counts := parsePharmacyCounts([]string{"В наличии", "Под заказ", "Забрать сегодня: более 10 аптек", "2 упаковки"})
	data, err := json.Marshal(counts)
	if err != nil || string(data) != `{"in_stock":null,"orderable":null}` {
		t.Fatalf("unknown counts must remain null: %s %v", data, err)
	}
	counts = parsePharmacyCounts([]string{"В наличии в 71 аптеке", "Под заказ в 93 аптеках", "В наличии в 71 аптеке"})
	data, _ = json.Marshal(counts)
	if string(data) != `{"in_stock":71,"orderable":93}` {
		t.Fatalf("duplicates or overlapping groups were added: %s", data)
	}
}

func TestConflictingOrOverflowingPharmacyCountsRemainUnknown(t *testing.T) {
	for _, lines := range [][]string{
		{"В наличии в 1 аптеке", "В наличии в 2 аптеках", "В наличии в 1 аптеке"},
		{"В наличии в " + strings.Repeat("9", 30) + " аптеках"},
	} {
		counts := parsePharmacyCounts(append(lines, "Под заказ в 93 аптеках"))
		if counts.InStock != nil || len(counts.Warnings) != 1 {
			t.Fatalf("misleading count accepted: %+v", counts)
		}
		assertPharmacyCount(t, counts.Orderable, 93)
	}
}

func TestRecordedSearchHasPharmacyCountsForEveryProduct(t *testing.T) {
	u, _ := url.Parse(siteOrigin + "/search/?q=магний+хелат")
	page, err := parseSearchPage(fixture(t, "magnesium-chelate"), u, "kazan")
	if err != nil {
		t.Fatal(err)
	}
	stock := []int{71, 93, 93, 38, 71, 9, 20, 4, 1, 4, 86, 78, 1, 1, 1}
	order := []int{93, -1, -1, -1, 93, -1, 93, 93, -1, -1, 93, 93, -1, -1, -1}
	if len(page.products) != len(stock) {
		t.Fatalf("wrong number of products: %d", len(page.products))
	}
	for i, item := range page.products {
		assertPharmacyCount(t, item.PharmacyCounts.InStock, stock[i])
		assertPharmacyCount(t, item.PharmacyCounts.Orderable, order[i])
		if item.ID == "20519511" && *item.PharmacyCounts.InStock != 1 {
			t.Fatal("GLS must retain its single-pharmacy supply")
		}
	}
}

func TestRecordedDetailRetainsCountsInDefaultAndFullOutput(t *testing.T) {
	u, _ := url.Parse(siteOrigin + "/kazan/catalog/test-15484411/")
	result, err := parseDetailPage(fixture(t, "magnesium-detail"), u, "kazan", "15484411")
	if err != nil {
		t.Fatal(err)
	}
	assertPharmacyCount(t, result.PharmacyCounts.InStock, 71)
	assertPharmacyCount(t, result.PharmacyCounts.Orderable, 93)
	result.concise()
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), `"pharmacy_counts":{"in_stock":71,"orderable":93}`) || len(result.Availability) != 2 {
		t.Fatalf("default detail lost structured counts or source text: %s", data)
	}
}

func assertPharmacyCount(t *testing.T, got *int, want int) {
	t.Helper()
	if want == -1 {
		if got != nil {
			t.Fatalf("expected unknown count, got %d", *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("expected count %d, got null", want)
	}
	if *got != want {
		t.Fatalf("expected count %d, got %d", want, *got)
	}
}
