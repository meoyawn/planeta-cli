package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

type compositionServing struct {
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
	Text     string  `json:"text"`
}

// Amounts retain the substance named by the label. They are not converted to
// active moieties, elemental quantities, daily doses, or chemical equivalents.
type compositionAmount struct {
	Substance  string              `json:"substance"`
	Amount     float64             `json:"amount"`
	Unit       string              `json:"unit"`
	Per        *compositionServing `json:"per,omitempty"`
	Qualifier  string              `json:"qualifier,omitempty"`
	SourceText string              `json:"source_text"`
}

var compositionServingPattern = regexp.MustCompile(`(?i)^(?:содержание\s+(?:активных\s+)?веществ\s+в\s+|в\s+)?([0-9]+(?:[.,][0-9]+)?)\s*(таблетк[а-яё]*|капсул[а-яё]*|мл|г)(?:\s|:|$)`)
var declaredAmountPattern = regexp.MustCompile(`(?i)(.*?)\s*([0-9]+(?:[.,][0-9]+)?)\s*\*?\s*(мкг|мг|ммоль|мл|ме|iu|mcg|mg|ml|г|л|g)(?:\s|[;,.):]|$)`)
var amountOnlyPattern = regexp.MustCompile(`(?i)^([0-9]+(?:[.,][0-9]+)?)\s*(мкг|мг|ммоль|мл|ме|iu|mcg|mg|ml|г|л|g)$`)
var amountRangePrefix = regexp.MustCompile(`[0-9]\s*[–—-]\s*$`)

func parseCompositionServing(text string) (*compositionServing, int) {
	match := compositionServingPattern.FindStringSubmatchIndex(text)
	if len(match) != 6 {
		return nil, 0
	}
	quantity := positiveLabelNumber(text[match[2]:match[3]])
	if quantity == 0 {
		return nil, 0
	}
	unit := strings.ToLower(text[match[4]:match[5]])
	switch {
	case strings.HasPrefix(unit, "таблетк"):
		unit = "таблетка"
	case strings.HasPrefix(unit, "капсул"):
		unit = "капсула"
	}
	return &compositionServing{Quantity: quantity, Unit: unit, Text: text}, match[1]
}

func parseCompositionAmounts(text string, tables [][][]string) []compositionAmount {
	var result []compositionAmount
	var serving *compositionServing
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// Tables are parsed from cells below, preserving each column's basis.
		if strings.Contains(line, "|") {
			serving = nil
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "содержание") || strings.HasSuffix(line, ":") {
			serving = nil
		}
		content := line
		if basis, end := parseCompositionServing(line); basis != nil {
			serving = basis
			content = strings.TrimSpace(line[end:])
			if strings.HasPrefix(strings.ToLower(content), "содержит:") {
				content = strings.TrimSpace(content[len("содержит:"):])
			}
		}
		for _, match := range declaredAmountPattern.FindAllStringSubmatch(content, -1) {
			if amountRangePrefix.MatchString(match[1]) {
				continue
			}
			name := strings.Trim(match[1], " \t,;:–—-*")
			qualifier := ""
			for _, prefix := range []string{"в том числе", "в пересчете на"} {
				if strings.HasPrefix(strings.ToLower(name), prefix+" ") {
					qualifier = prefix
					name = strings.TrimSpace(name[len(prefix):])
					break
				}
			}
			// Narrative equivalence footnotes and ranges are retained as source
			// text; they are not turned into named substance amounts.
			if name == "" || strings.ContainsAny(name, ":,;") || strings.HasPrefix(name, "количество") || positiveLabelNumber(name) != 0 {
				continue
			}
			amount := positiveLabelNumber(match[2])
			if amount == 0 {
				continue
			}
			result = append(result, compositionAmount{
				Substance: name, Amount: amount, Unit: strings.ToLower(match[3]),
				Per: serving, Qualifier: qualifier, SourceText: line,
			})
		}
	}
	for _, table := range tables {
		if len(table) < 2 {
			continue
		}
		headers := table[0]
		for _, row := range table[1:] {
			if len(row) < 2 {
				continue
			}
			for column, cell := range row[1:] {
				match := amountOnlyPattern.FindStringSubmatch(strings.TrimSpace(cell))
				if len(match) != 3 || positiveLabelNumber(match[1]) == 0 {
					continue
				}
				var basis *compositionServing
				if column+1 < len(headers) {
					basis, _ = parseCompositionServing(strings.TrimSpace(headers[column+1]))
				}
				result = append(result, compositionAmount{
					Substance: row[0], Amount: positiveLabelNumber(match[1]), Unit: strings.ToLower(match[2]),
					Per: basis, SourceText: strings.Join(headers, " | ") + "\n" + strings.Join(row, " | "),
				})
			}
		}
	}
	return result
}

func positiveLabelNumber(value string) float64 {
	number, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64)
	if err != nil || number <= 0 || math.IsInf(number, 0) || math.IsNaN(number) {
		return 0
	}
	return number
}
