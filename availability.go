package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Counts refer to pharmacies in the requested city, not packs. The two groups
// may overlap, so no total is inferred. A missing declaration remains null.
type pharmacyCounts struct {
	InStock   *int     `json:"in_stock"`
	Orderable *int     `json:"orderable"`
	Warnings  []string `json:"warnings,omitempty"`
}

var inStockPharmacies = regexp.MustCompile(`(?i)^(?:забрать сегодня:\s*|в наличии в\s+)([0-9]+(?: [0-9]{3})*)\s+аптек(?:а|и|е|ах)?\.?$`)
var orderablePharmacies = regexp.MustCompile(`(?i)^(?:завтра и позже:\s*|под заказ в\s+)([0-9]+(?: [0-9]{3})*)\s+аптек(?:а|и|е|ах)?\.?$`)

func parsePharmacyCounts(availability []string) pharmacyCounts {
	var inStock, orderable pharmacyCounter
	for _, text := range availability {
		text = cleanText(text)
		if match := inStockPharmacies.FindStringSubmatch(text); len(match) == 2 {
			inStock.add(match[1])
		}
		if match := orderablePharmacies.FindStringSubmatch(text); len(match) == 2 {
			orderable.add(match[1])
		}
	}
	result := pharmacyCounts{InStock: inStock.value, Orderable: orderable.value}
	if inStock.invalid {
		result.InStock = nil
		result.Warnings = append(result.Warnings, "Invalid or conflicting in-stock pharmacy counts; inspect availability text.")
	}
	if orderable.invalid {
		result.Orderable = nil
		result.Warnings = append(result.Warnings, "Invalid or conflicting orderable pharmacy counts; inspect availability text.")
	}
	return result
}

type pharmacyCounter struct {
	value   *int
	invalid bool
}

func (c *pharmacyCounter) add(text string) {
	count, err := strconv.Atoi(strings.ReplaceAll(text, " ", ""))
	if err != nil || (c.value != nil && *c.value != count) {
		c.invalid = true
		return
	}
	c.value = &count
}
