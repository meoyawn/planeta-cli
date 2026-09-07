package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

type namedLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type attribute struct {
	Name  string      `json:"name"`
	Value string      `json:"value"`
	Links []namedLink `json:"links,omitempty"`
}

type instruction struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Text   string       `json:"text"`
	Tables [][][]string `json:"tables,omitempty"`
	Links  []namedLink  `json:"links,omitempty"`
}

type detailResult struct {
	product
	City               string              `json:"city"`
	SourceURL          string              `json:"source_url"`
	FetchedAt          time.Time           `json:"fetched_at"`
	HTTPRequests       int                 `json:"http_requests"`
	ActiveIngredient   string              `json:"active_ingredient,omitempty"`
	DosageForm         string              `json:"dosage_form,omitempty"`
	Ingredients        string              `json:"ingredients,omitempty"`
	ActiveSubstances   string              `json:"active_substances,omitempty"`
	Excipients         string              `json:"excipients,omitempty"`
	CompositionAmounts []compositionAmount `json:"composition_amounts,omitempty"`
	CompositionTables  [][][]string        `json:"composition_tables,omitempty"`
	Dosage             string              `json:"dosage,omitempty"`
	Directions         string              `json:"directions,omitempty"`
	PackageQuantity    string              `json:"package_quantity,omitempty"`
	Package            *packageInfo        `json:"package,omitempty"`
	PackageUnitPrice   *packageUnitPrice   `json:"package_unit_price,omitempty"`
	Age                string              `json:"age,omitempty"`
	Specifications     []attribute         `json:"specifications"`
	Instructions       []instruction       `json:"instructions,omitempty"`
	Images             []string            `json:"images"`
	Breadcrumbs        []namedLink         `json:"breadcrumbs,omitempty"`
	Variants           []product           `json:"variants"`
	SourceSchema       json.RawMessage     `json:"source_schema,omitempty"`
	Warnings           []string            `json:"warnings,omitempty"`
}

func parseDetailPage(body []byte, source *url.URL, city, id string) (*detailResult, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("read HTML: %w", err)
	}
	root := doc.Find(".product-detail").First()
	if root.Length() == 0 {
		return nil, fmt.Errorf("product detail missing; site markup may have changed")
	}
	result := &detailResult{
		product: product{ID: id, URL: source.String(), Availability: []string{}},
		City:    city, SourceURL: source.String(),
		Specifications: []attribute{}, Instructions: []instruction{}, Images: []string{}, Variants: []product{},
	}
	for _, script := range doc.Find("script[type='application/ld+json']").EachIter() {
		var value any
		if json.Unmarshal([]byte(script.Text()), &value) == nil {
			if schema := findProductSchema(value, id); schema != nil {
				result.SourceSchema, _ = json.Marshal(schema)
				result.Name, _ = schema["name"].(string)
				result.ActiveIngredient, _ = schema["activeIngredient"].(string)
				result.DosageForm, _ = schema["dosageForm"].(string)
				result.Prescription, _ = schema["prescriptionStatus"].(string)
				if images, ok := schema["image"].([]any); ok {
					for _, image := range images {
						if raw, ok := image.(string); ok {
							result.Images = appendURL(result.Images, source, raw)
						}
					}
				}
				break
			}
		}
	}
	pageID := root.Closest("[data-id]").AttrOr("data-id", "")
	if pageID != "" && pageID != id {
		return nil, fmt.Errorf("requested id %s but page identifies product %s", id, pageID)
	}
	if pageID == "" && len(result.SourceSchema) == 0 {
		return nil, fmt.Errorf("cannot verify product id %s in the returned page", id)
	}
	if result.Name == "" {
		result.Name = cleanText(doc.Find("h1").First().Text())
	}
	if result.Name == "" {
		return nil, fmt.Errorf("product name missing")
	}
	result.PriceFrom = visiblePrice(root.Find(".product-detail__price").First().Text())
	if result.PriceFrom != nil {
		result.Currency = "RUB"
	}
	result.OldPrice = visiblePrice(root.Find(".product-card__price-old").First().Text())
	result.Labels = selectionTexts(root.Find(".product-detail__label-list .this-label"))
	result.Expiry = cleanText(root.Find(".this-expiration_text").First().Text())
	result.Availability = selectionTexts(root.Find(".product-detail__availability p"))
	result.PharmacyCounts = parsePharmacyCounts(result.Availability)
	result.SourceText = readableText(root.Find(".product-detail__wrap").First())
	if text := cleanText(root.Find(".this-recipe").First().Text()); text != "" {
		result.Prescription = text
	}
	for _, row := range root.Find(".product-detail__spec tr").EachIter() {
		cells := row.Find("td")
		if cells.Length() < 2 {
			continue
		}
		name := strings.TrimSuffix(cleanText(cells.Eq(0).Text()), ":")
		value := cleanText(cells.Eq(1).Text())
		result.Specifications = append(result.Specifications, attribute{Name: name, Value: value, Links: pageLinks(cells.Eq(1), source)})
		switch name {
		case "Бренд":
			result.Brand = value
		case "Завод производитель":
			result.Manufacturer = value
		case "Производитель":
			if result.Manufacturer == "" {
				result.Manufacturer = value
			}
		case "Форма выпуска":
			result.DosageForm = value
		case "Действующие вещества":
			result.ActiveIngredient = value
		case "Количество в упаковке":
			result.PackageQuantity = value
		case "Возраст":
			result.Age = value
		case "Дозировка":
			result.Dosage = value
		}
	}
	for _, section := range doc.Find(".product-detail-description-content__item[id]").EachIter() {
		content := section.Find(".product-detail-description-content__item-content").First()
		if content.Length() == 0 {
			continue
		}
		item := instruction{
			ID: section.AttrOr("id", ""), Title: cleanText(section.Find("h3").First().Text()),
			Text: readableText(content), Links: pageLinks(content, source),
		}
		for _, table := range content.Find("table").EachIter() {
			var rows [][]string
			for _, row := range table.Find("tr").EachIter() {
				var cells []string
				for _, cell := range row.ChildrenFiltered("th,td").EachIter() {
					cells = append(cells, readableText(cell))
				}
				if len(cells) > 0 {
					rows = append(rows, cells)
				}
			}
			if len(rows) > 0 {
				item.Tables = append(item.Tables, rows)
			}
		}
		result.Instructions = append(result.Instructions, item)
		switch item.ID {
		case "instruction_COMPOSITION":
			result.Ingredients = compositionText(content)
			result.ActiveSubstances, result.Excipients = ingredientParts(result.Ingredients)
			result.CompositionTables = item.Tables
			composition := result.ActiveSubstances
			if composition == "" {
				composition = result.Ingredients
			}
			result.CompositionAmounts = parseCompositionAmounts(composition, item.Tables)
		case "instruction_DOSAGE":
			result.Dosage = item.Text
		case "instruction_USEMETHODANDDOSES":
			result.Directions = item.Text
		}
	}
	if len(result.Instructions) == 0 {
		result.Warnings = append(result.Warnings, "The page supplied no instruction sections; do not infer ingredients or dosing from the product name.")
	}
	if result.Ingredients == "" {
		result.Warnings = append(result.Warnings, "The page does not publish ingredient information.")
	}
	if result.Dosage == "" {
		result.Dosage = declaredServingContents(result.Ingredients)
	}
	result.addPackageFacts()
	for _, img := range root.Find(".product-detail__image img, .product-detail__img, [data-img]").EachIter() {
		result.Images = appendURL(result.Images, source, img.AttrOr("data-img", img.AttrOr("data-src", img.AttrOr("src", ""))))
	}
	if len(result.Images) > 0 {
		result.ImageURL = result.Images[0]
	}
	result.Breadcrumbs = pageLinks(doc.Find(".nav-bread-crumbs"), source)
	if len(result.Breadcrumbs) > 0 {
		result.Category = result.Breadcrumbs[len(result.Breadcrumbs)-1].Text
	}
	seen := map[string]bool{}
	for _, card := range doc.Find("#other_form .item-card").EachIter() {
		item, err := parseProduct(card, source, city)
		if err != nil {
			result.Warnings = append(result.Warnings, "A related package card could not be parsed.")
			continue
		}
		if item.ID != id && !seen[item.ID] {
			seen[item.ID] = true
			result.Variants = append(result.Variants, item)
		}
	}
	return result, nil
}

func (r *detailResult) concise() {
	r.Instructions = nil
	r.Directions = ""
	r.SourceSchema = nil
	r.SourceText = ""
	for i := range r.Variants {
		r.Variants[i].SourceText = ""
	}
}

// Composition pages often mix ingredients with marketing under bold headings.
// Keep the ingredient and nutrient text; the unabridged section is in --full.
func compositionText(content *goquery.Selection) string {
	clone := content.Clone()
	for _, heading := range clone.Find("b,strong").EachIter() {
		heading.PrependHtml("<br>")
		heading.AppendHtml("<br>")
	}
	var lines []string
	include := true
	for _, line := range strings.Split(readableText(clone), "\n") {
		label := strings.ToLower(strings.TrimSuffix(line, ":"))
		switch label {
		case "описание", "форма выпуска":
			include = false
		case "активное вещество", "активные вещества", "вспомогательные вещества", "состав", "содержание активных веществ":
			include = true
		}
		if include {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// Keep the complete published excipient block, including ingredient warnings.
// No additive is classified as safe/unsafe and absent declarations stay absent.
func ingredientParts(ingredients string) (string, string) {
	var active, excipients []string
	var target *[]string
	for _, line := range strings.Split(ingredients, "\n") {
		switch strings.ToLower(strings.TrimSuffix(line, ":")) {
		case "активное вещество", "активные вещества":
			target = &active
			continue
		case "вспомогательные вещества":
			target = &excipients
			continue
		}
		if target != nil {
			*target = append(*target, line)
		}
	}
	return strings.Join(active, "\n"), strings.Join(excipients, "\n")
}

// Some supplements omit a dedicated dosage section but explicitly state the
// nutrient contents of one tablet/capsule in composition. Preserve that entire
// statement and its serving size; never turn compound mass into elemental dose.
func declaredServingContents(ingredients string) string {
	var lines []string
	for _, line := range strings.Split(ingredients, "\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "содержание активных веществ в ") {
			if len(lines) > 0 {
				break
			}
			lines = append(lines, line)
			continue
		}
		if len(lines) > 0 {
			if strings.HasSuffix(line, ":") {
				break
			}
			lines = append(lines, line)
		}
	}
	if len(lines) < 2 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// Only retain the public schema for this exact product, never unrelated scripts.
func findProductSchema(value any, id string) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		if fmt.Sprint(v["sku"]) == id {
			return v
		}
		if graph, ok := v["@graph"]; ok {
			return findProductSchema(graph, id)
		}
	case []any:
		for _, child := range v {
			if found := findProductSchema(child, id); found != nil {
				return found
			}
		}
	}
	return nil
}

func pageLinks(selection *goquery.Selection, base *url.URL) []namedLink {
	var links []namedLink
	seen := map[string]bool{}
	for _, link := range selection.Find("a[href]").EachIter() {
		href := link.AttrOr("href", "")
		if href == "" || strings.HasPrefix(href, "#") {
			continue
		}
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		u := base.ResolveReference(parsed)
		if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		links = append(links, namedLink{Text: cleanText(link.Text()), URL: u.String()})
	}
	return links
}

func appendURL(urls []string, base *url.URL, raw string) []string {
	if raw == "" {
		return urls
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return urls
	}
	resolved := base.ResolveReference(parsed)
	if (resolved.Scheme != "https" && resolved.Scheme != "http") || resolved.User != nil {
		return urls
	}
	for _, existing := range urls {
		if existing == resolved.String() {
			return urls
		}
	}
	return append(urls, resolved.String())
}

// Preserve meaningful boundaries in prose, lists, and tables, unlike .Text(),
// which concatenates "200 mg" and the next dosage cell with no separator.
func readableText(selection *goquery.Selection) string {
	clone := selection.Clone()
	clone.Find("script,style,noscript,svg,form,button,input,select,textarea,.card-script-counter,.product-detail__action").Remove()
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			out.WriteString(node.Data)
			return
		}
		block := false
		if node.Type == html.ElementNode {
			switch node.Data {
			case "br", "p", "div", "li", "ul", "ol", "tr", "h1", "h2", "h3", "h4", "table":
				block = true
				out.WriteByte('\n')
			case "td", "th":
				out.WriteString(" | ")
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if block {
			out.WriteByte('\n')
		}
	}
	for _, node := range clone.Nodes {
		walk(node)
	}
	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		if clean := strings.Trim(cleanText(line), " |"); clean != "" {
			lines = append(lines, clean)
		}
	}
	return strings.Join(lines, "\n")
}
