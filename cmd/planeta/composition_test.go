package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRecordedPlainIDCompositionLabels(t *testing.T) {
	data, err := os.ReadFile("testdata/composition-labels.json")
	if err != nil {
		t.Fatal(err)
	}
	var labels []struct {
		ID          string  `json:"id"`
		Composition string  `json:"composition"`
		Expected    float64 `json:"expected_magnesium_mg_per_piece"`
	}
	if err := json.Unmarshal(data, &labels); err != nil {
		t.Fatal(err)
	}
	if len(labels) != 15 {
		t.Fatalf("expected all 15 recorded products, got %d", len(labels))
	}
	for _, label := range labels {
		t.Run(label.ID, func(t *testing.T) {
			found := false
			for _, amount := range parseCompositionAmounts(label.Composition, nil) {
				if !strings.EqualFold(amount.Substance, "Магний") {
					continue
				}
				if amount.Per == nil || amount.Unit != "мг" || amount.Amount/amount.Per.Quantity != label.Expected {
					t.Fatalf("elemental declaration has incorrect basis or amount: %+v", amount)
				}
				found = true
			}
			if found != (label.Expected != 0) {
				t.Fatalf("missing or invented elemental declaration: %+v", parseCompositionAmounts(label.Composition, nil))
			}
		})
	}
}

func TestCompositionAmountsPreserveDistinctSubstancesAndServing(t *testing.T) {
	text := "Содержание активных веществ в 1 капсуле:\nМагния бисглицинат 250 мг, в том числе магний 50 мг;\nВитамин В6 0,75 мг"
	got := parseCompositionAmounts(text, nil)
	if len(got) != 3 {
		t.Fatalf("lost declarations: %+v", got)
	}
	for i, want := range []struct {
		name string
		mg   float64
	}{{"Магния бисглицинат", 250}, {"магний", 50}, {"Витамин В6", .75}} {
		if got[i].Substance != want.name || got[i].Amount != want.mg || got[i].Unit != "мг" || got[i].Per == nil || got[i].Per.Quantity != 1 || got[i].Per.Unit != "капсула" {
			t.Fatalf("declaration %d changed: %+v", i, got[i])
		}
	}
	if got[1].Qualifier != "в том числе" || !strings.Contains(got[1].SourceText, "250 мг") {
		t.Fatal("lost compound/constituent relationship or source")
	}
}

func TestCompositionAmountsWorkForOtherDrugsAndSupplementUnits(t *testing.T) {
	for _, test := range []struct {
		text, substance, unit, perUnit string
		amount, perQuantity            float64
	}{
		{"1 таблетка содержит: парацетамол 500 мг.", "парацетамол", "мг", "таблетка", 500, 1},
		{"В 5 мл:\nАмоксициллин 250 мг", "Амоксициллин", "мг", "мл", 250, 5},
		{"Содержание активных веществ в 2 капсулах:\nЙод 150 мкг", "Йод", "мкг", "капсула", 150, 2},
		{"В 1 таблетке:\nВитамин D3 2000 МЕ", "Витамин D3", "ме", "таблетка", 2000, 1},
	} {
		t.Run(test.substance, func(t *testing.T) {
			got := parseCompositionAmounts(test.text, nil)
			if len(got) != 1 || got[0].Substance != test.substance || got[0].Amount != test.amount || got[0].Unit != test.unit || got[0].Per == nil || got[0].Per.Quantity != test.perQuantity || got[0].Per.Unit != test.perUnit {
				t.Fatalf("incorrect declaration: %+v", got)
			}
		})
	}
}

func TestCompositionAmountsDoNotInventBasisOrConvertCompoundMass(t *testing.T) {
	got := parseCompositionAmounts("Магния бисглицинат 250 мг", nil)
	if len(got) != 1 || got[0].Substance != "Магния бисглицинат" || got[0].Per != nil {
		t.Fatalf("invented elemental content or serving: %+v", got)
	}
	got = parseCompositionAmounts("Содержание активных веществ в 2–4 таблетках:\nКальций 200 мг", nil)
	if len(got) != 1 || got[0].Per != nil {
		t.Fatalf("converted a serving range to a scalar: %+v", got)
	}
	got = parseCompositionAmounts("1 таблетка содержит:\nКальций 200 мг\nСодержание активных веществ в суточной дозе:\nКальций 600 мг", nil)
	if len(got) != 2 || got[0].Per == nil || got[1].Per != nil {
		t.Fatalf("carried an old serving into an unknown basis: %+v", got)
	}
	for _, text := range []string{"Витамин С 90–100 мг", "* – количество, эквивалентное 100 мг магния (Mg 2+)"} {
		if got := parseCompositionAmounts(text, nil); len(got) != 0 {
			t.Fatalf("turned an ambiguous statement into a named amount: %q %+v", text, got)
		}
	}
}

func TestCompositionTableAmountsUseTheirOwnColumnBasis(t *testing.T) {
	table := [][]string{{"Вещество", "В 1 капсуле", "В 2 капсулах"}, {"Кальций", "100 мг", "200 мг"}, {"Витамин D3", "10 мкг", "20 мкг"}}
	got := parseCompositionAmounts("Вещество | В 1 капсуле | В 2 капсулах\nКальций | 100 мг | 200 мг", [][][]string{table})
	if len(got) != 4 {
		t.Fatalf("duplicated or lost table declarations: %+v", got)
	}
	for i, amount := range got {
		wantServing := float64(i%2 + 1)
		if amount.Per == nil || amount.Per.Quantity != wantServing || amount.Per.Unit != "капсула" || !strings.Contains(amount.SourceText, "Вещество |") {
			t.Fatalf("wrong column basis: %+v", amount)
		}
	}
}
