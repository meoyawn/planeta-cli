package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

type packageInfo struct {
	Quantity   float64 `json:"quantity"`
	Unit       string  `json:"unit"`
	Source     string  `json:"source"`
	SourceText string  `json:"source_text"`
}

type packageUnitPrice struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
}

var packageLabel = regexp.MustCompile(`^([0-9]+(?:[.,][0-9]+)?)\s*(шт|мл|г|кг|л)\.?$`)
var piecesInName = regexp.MustCompile(`(?i)(?:^|\s)([1-9][0-9]*)\s*шт(?:\.|\s|$)`)

func (r *detailResult) addPackageFacts() {
	label := r.PackageQuantity
	source := "specifications"
	if label == "" {
		// Only an explicit, unambiguous piece count can be recovered from a
		// title. Strengths such as 200 mg are never treated as package size.
		matches := piecesInName.FindAllStringSubmatch(r.Name, -1)
		if len(matches) != 1 {
			return
		}
		label = matches[0][1] + " шт"
		source = "name"
		r.PackageQuantity = label
	}
	match := packageLabel.FindStringSubmatch(strings.ToLower(cleanText(label)))
	if len(match) != 3 {
		return
	}
	quantity, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
	if err != nil || quantity <= 0 || math.IsInf(quantity, 0) {
		return
	}
	r.Package = &packageInfo{Quantity: quantity, Unit: match[2], Source: source, SourceText: label}
	if source == "name" {
		r.Package.SourceText = r.Name
	}
	if r.PriceFrom == nil || r.Currency == "" {
		return
	}
	// Prices are in currency units. Round the per-unit price to minor units;
	// multiply before dividing to avoid the 2163/120 -> 18.02 tie error.
	amount := math.Round(*r.PriceFrom*100/quantity) / 100
	r.PackageUnitPrice = &packageUnitPrice{Amount: amount, Currency: r.Currency, Quantity: 1, Unit: match[2]}
}
