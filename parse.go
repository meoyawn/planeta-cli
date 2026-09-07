package main

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type searchResult struct {
	Query        string    `json:"query"`
	City         string    `json:"city"`
	SourceURL    string    `json:"source_url"`
	Total        int       `json:"total"`
	Count        int       `json:"count"`
	Page         int       `json:"page"`
	TotalPages   int       `json:"total_pages"`
	HasMore      bool      `json:"has_more"`
	NextPage     *int      `json:"next_page"`
	NextURL      string    `json:"next_url,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`
	HTTPRequests int       `json:"http_requests"`
	Results      []product `json:"results"`
}

type product struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	URL            string         `json:"url"`
	Manufacturer   string         `json:"manufacturer,omitempty"`
	PriceFrom      *float64       `json:"price_from"`
	Currency       string         `json:"currency,omitempty"`
	ImageURL       string         `json:"image_url,omitempty"`
	Availability   []string       `json:"availability"`
	PharmacyCounts pharmacyCounts `json:"pharmacy_counts"`
	Brand          string         `json:"brand,omitempty"`
	Category       string         `json:"category,omitempty"`
	OldPrice       *float64       `json:"old_price,omitempty"`
	Labels         []string       `json:"labels,omitempty"`
	Expiry         string         `json:"expiry,omitempty"`
	Prescription   string         `json:"prescription,omitempty"`
	Delivery       []string       `json:"delivery,omitempty"`
	SourceText     string         `json:"source_text,omitempty"`
}

type searchPage struct {
	total       int
	products    []product
	links       []*url.URL
	totalPages  int
	currentPage int
}

var resultCount = regexp.MustCompile(`(?i)\sнайден[аоы]?\s+([0-9][0-9\s]*)\s+товар(?:а|ов)?$`)
var productID = regexp.MustCompile(`-(-?[0-9]+)/?$`)
var rublePrice = regexp.MustCompile(`^(?:от\s*)?([0-9][0-9\s]*(?:[.,][0-9]{1,2})?)\s*₽$`)

func parseSearchPage(body []byte, pageURL *url.URL, city string) (*searchPage, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("read HTML: %w", err)
	}
	heading := cleanText(doc.Find("h1").First().Text())
	countMatch := resultCount.FindStringSubmatch(heading)
	page := &searchPage{products: []product{}, totalPages: 1}
	// Empty searches still display recommended products and their pagination.
	// Those are not matches for the query and must not enter the result set.
	if strings.HasPrefix(strings.ToLower(heading), "по запросу") && (strings.HasSuffix(strings.ToLower(heading), "ничего не найдено") || strings.HasPrefix(strings.ToLower(heading), "по запросу ничего не найдено")) {
		return page, nil
	}
	if len(countMatch) == 2 {
		page.total, err = strconv.Atoi(strings.Join(strings.Fields(countMatch[1]), ""))
		if err != nil {
			return nil, fmt.Errorf("invalid result total in heading %q", heading)
		}
	} else {
		return nil, fmt.Errorf("search result count missing from heading %q; site markup may have changed", heading)
	}
	if value, ok := doc.Find(".pagination[data-count_element]").First().Attr("data-count_element"); ok {
		count, err := strconv.Atoi(value)
		if err != nil || count != page.total {
			return nil, fmt.Errorf("pagination count %q disagrees with heading total %d", value, page.total)
		}
	}
	// Discounted batches near their expiry date omit all schema.org attributes,
	// but are distinct search results with the same visible card classes.
	cards := doc.Find(`[data-name="Результаты_поиска"] .item-card`)
	seen := map[string]bool{}
	for _, card := range cards.EachIter() {
		item, err := parseProduct(card, pageURL, city)
		if err != nil {
			return nil, err
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("duplicate product id %s on search page", item.ID)
		}
		seen[item.ID] = true
		page.products = append(page.products, item)
	}
	if page.total > 0 && len(page.products) == 0 {
		return nil, fmt.Errorf("site reports %d results but product cards are missing", page.total)
	}
	for _, link := range doc.Find(".pagination a[href]").EachIter() {
		href, _ := link.Attr("href")
		parsed, err := url.Parse(href)
		if err != nil {
			return nil, fmt.Errorf("invalid pagination link: %w", err)
		}
		resolved := pageURL.ResolveReference(parsed)
		if resolved.Scheme != pageURL.Scheme || resolved.Host != pageURL.Host || resolved.Path != pageURL.Path || resolved.Query().Get("q") != pageURL.Query().Get("q") || pageNumber(resolved) < 1 {
			return nil, fmt.Errorf("invalid pagination link outside the current search")
		}
		page.links = append(page.links, resolved)
		page.totalPages = max(page.totalPages, pageNumber(resolved))
	}
	if active := cleanText(doc.Find(".pagination__item_active").First().Text()); active != "" {
		page.currentPage, err = strconv.Atoi(active)
		if err != nil || page.currentPage < 1 {
			return nil, fmt.Errorf("invalid active page %q", active)
		}
		page.totalPages = max(page.totalPages, page.currentPage)
	}
	if len(page.products) > page.total || (page.totalPages == 1 && len(page.products) != page.total) {
		return nil, fmt.Errorf("incomplete or inconsistent page: site reports %d products, page has %d and %d pages", page.total, len(page.products), page.totalPages)
	}
	return page, nil
}

func parseProduct(card *goquery.Selection, pageURL *url.URL, city string) (product, error) {
	item := product{
		Name:         cleanText(card.Find(`[itemprop="name"]`).First().Text()),
		Manufacturer: cleanText(card.Find(".item-card-manufacturer-text").First().Text()),
		Availability: []string{},
	}
	link := card.Find("a.item-card-title-text").First()
	if item.Name == "" {
		item.Name = cleanText(link.Text())
	}
	href, _ := link.Attr("href")
	parsed, err := url.Parse(href)
	if err != nil || href == "" || item.Name == "" {
		return item, fmt.Errorf("product card is missing a valid name or URL")
	}
	resolved := pageURL.ResolveReference(parsed)
	if resolved.Scheme != pageURL.Scheme || resolved.Host != pageURL.Host || !strings.HasPrefix(resolved.Path, "/"+city+"/catalog/") {
		return item, fmt.Errorf("product %q is outside requested city %q", item.Name, city)
	}
	id := productID.FindStringSubmatch(resolved.Path)
	if len(id) != 2 {
		return item, fmt.Errorf("product %q has no recognizable ID in its URL", item.Name)
	}
	item.ID, item.URL = id[1], resolved.String()
	item.Brand = cleanText(card.Find(`input[name="brand"]`).First().AttrOr("value", ""))
	item.Category = cleanText(card.Find(`input[name="category"]`).First().AttrOr("value", ""))
	item.Labels = selectionTexts(card.Find(".this-label"))
	item.Expiry = cleanText(card.Find(".this-expiration_text").Text())
	item.Prescription = cleanText(card.Find(".this-recipe").Text())
	item.Delivery = selectionTexts(card.Find(".item-card-action__text__v1"))
	item.OldPrice = visiblePrice(card.Find(".item-card-price-old").First().Text())
	item.SourceText = readableText(card)
	priceElement := card.Find(".item-card-price-number").First()
	if priceElement.Length() == 0 {
		priceElement = card.Find(`[itemprop="price"]`).First()
	}
	priceText, hasPrice := priceElement.Attr("content")
	if !hasPrice && cleanText(priceElement.Text()) != "" {
		match := rublePrice.FindStringSubmatch(cleanText(priceElement.Text()))
		if len(match) != 2 {
			return item, fmt.Errorf("product %q has unrecognized price text", item.ID)
		}
		priceText = strings.ReplaceAll(strings.Join(strings.Fields(match[1]), ""), ",", ".")
		hasPrice = true
	}
	if hasPrice {
		price, err := strconv.ParseFloat(priceText, 64)
		if err != nil || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return item, fmt.Errorf("product %q has invalid price %q", item.ID, priceText)
		}
		item.PriceFrom = &price
		item.Currency, _ = card.Find(`[itemprop="priceCurrency"]`).First().Attr("content")
		if item.Currency == "" && strings.Contains(priceElement.Text(), "₽") {
			item.Currency = "RUB"
		}
	}
	image := card.Find(".item-card-image img, [itemprop=image]").First()
	imageURL := image.AttrOr("data-src", image.AttrOr("src", ""))
	if imageURL != "" {
		parsedImage, err := url.Parse(imageURL)
		if err == nil {
			resolvedImage := pageURL.ResolveReference(parsedImage)
			if resolvedImage.Scheme == "https" || resolvedImage.Scheme == "http" {
				item.ImageURL = resolvedImage.String()
			}
		}
	}
	for _, availability := range card.Find(".item-card-availability-text").EachIter() {
		if text := cleanText(availability.Text()); text != "" {
			item.Availability = append(item.Availability, text)
		}
	}
	item.PharmacyCounts = parsePharmacyCounts(item.Availability)
	return item, nil
}

func cleanText(s string) string { return strings.Join(strings.Fields(s), " ") }

func selectionTexts(s *goquery.Selection) []string {
	var result []string
	seen := map[string]bool{}
	for _, item := range s.EachIter() {
		if text := cleanText(item.Text()); text != "" && !seen[text] {
			result = append(result, text)
			seen[text] = true
		}
	}
	return result
}

func visiblePrice(s string) *float64 {
	match := rublePrice.FindStringSubmatch(cleanText(s))
	if len(match) != 2 {
		return nil
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(strings.Join(strings.Fields(match[1]), ""), ",", "."), 64)
	if err != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return nil
	}
	return &value
}
