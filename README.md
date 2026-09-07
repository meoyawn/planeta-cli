# planeta

An HTTP-only Go CLI for the public [Planeta Zdorovo catalog](https://planetazdorovo.ru/kazan/), with JSON output and Kazan as the default city. Requires Go 1.25+ to build.

## Install and import browser clearance

```sh
go install github.com/meoyawn/planeta-cli/cmd/planeta@latest
planeta auth import --browser chrome
planeta search магний хелат
```

Open the site in your signed-out browser and let its normal browser check finish before importing. No pharmacy account is required.

Import uses [Sweet Cookie v0.0.2](https://github.com/steipete/sweetcookie), following the browser-import approach in [spogo](https://github.com/openclaw/spogo). It reads the selected browser's local cookie store; it does not launch or automate a browser. macOS may show a Keychain prompt.

```sh
planeta auth import --browser firefox
planeta auth import --browser chrome --browser-profile "Profile 1"
planeta auth status
```

Other supported browser names include brave, edge, chromium, safari, arc, vivaldi, opera, helium, dia, comet, atlas, whale, zen, floorp, waterfox, and librewolf. Availability depends on the browser and operating system.

Only `qrator_jsid`, `qrator_jsid2`, `city_id`, `city_code`, and `region_id` for `planetazdorovo.ru` are imported. Account cookies are excluded. Import and status output cookie names, expiry, and store location, never values. Failed imports preserve the previous store.

The CLI manages `auth.json` automatically under `os.UserConfigDir()/planeta/`: on macOS, `~/Library/Application Support/planeta/auth.json`; on Linux, normally `~/.config/planeta/auth.json`. The file has owner-only permissions. There is no manual cookie-file maintenance.

Qrator clearance still expires and may depend on your network and browser. Import copies an existing clearance; it cannot renew an expired browser challenge. When requested, refresh the site in that browser and repeat `auth import`.

## Search

```sh
planeta search магний хелат
planeta search --page 2 магний
planeta search --city kazan "магний & B6"
```

Each invocation returns **one page** in the website's default order:

- `total`: all matches reported by the website.
- `count`: products returned on this page.
- `page`, `total_pages`, `has_more`, `next_page`, `next_url`: explicit pagination metadata.
- `results`: product IDs, canonical URLs, names, manufacturer, brand, category, price, old price, currency, image URL, availability text, numeric `pharmacy_counts`, delivery, promotion labels, expiry warnings, and visible card text where supplied.
- `source_url`, `fetched_at`, `http_requests`: provenance and request count.

Empty searches return `total: 0`, `count: 0`, `results: []`, `has_more: false`, and `next_page: null`. Recommendations shown by the site after an unsuccessful search are excluded. An empty search has one empty page.

Discounted batches can have negative IDs and remain separate products.

Both search products and `id` output expose supply in the selected city:

```json
"pharmacy_counts": {
  "in_stock": 1,
  "orderable": null
}
```

`in_stock` comes from “Забрать сегодня” on search cards or “В наличии” on product pages. `orderable` comes from “Завтра и позже” or “Под заказ”. The original wording remains in `availability`. A count the page does not publish or that cannot be parsed is `null`; only an explicit numeric zero becomes `0`. Conflicting declarations also stay `null` and produce a warning inside `pharmacy_counts`.

These are counts of pharmacies, not packs. The two groups may overlap, so the CLI does not add them into a total. Counts do not establish stock quantity, proximity, or price at each pharmacy. Use `fetched_at` to assess freshness; no pharmacy list or extra AJAX requests are fetched.

```sh
planeta search магний хелат | jq '.results[] | {id, name, price_from, pharmacy_counts}'
planeta id 20519511 | jq '{name, pharmacy_counts, availability}'
```

Callers can apply their own minimum pharmacy-count requirement outside the CLI.

## Product details

```sh
planeta id 15484411
planeta id 15484411 --full
planeta id -157028
```

The default output contains the facts needed to compare products: the complete `ingredients` text, separate `active_substances` and `excipients` fields, dosage strength and declared serving amounts, manufacturer, brand, dosage form, package quantity, age indication, specifications, price, availability, images, links, and related package cards present in the same HTML. The `excipients` field preserves the full list and attached ingredient warnings, including sorbitol/sweetener statements; it does not summarize away additives.

`composition_amounts` exposes clearly declared substance names, numeric amounts, measurement units, explicit serving bases (`per`), and source text. This is generic label extraction: compound amounts and any separately declared constituent amounts remain distinct entries. For example, a label can declare both 250 mg magnesium bisglycinate and “including” 50 mg magnesium; the latter retains `qualifier: "в том числе"`. No elemental conversion or daily requirement is inferred. An unclear serving basis stays absent. Ranges, narrative footnotes, and unsupported layouts remain available in `ingredients`; `composition_tables` preserves the original composition table cells even without `--full`. Parsed amounts are a partial view of the source, not a substitute for its complete label.

`package` exposes the numeric quantity, unit, and source (`specifications` or an explicit piece count in the product `name`). `package_unit_price` gives the current “from” price per piece, ml, or other declared package unit, rounded to two decimals. Unclear package sizes are left unparsed. Missing ingredient information is flagged so a product with no label cannot appear to be additive-free.

Inspect product facts without loading the complete instructions:

```sh
planeta id 23162311 | jq '{name, composition_amounts, active_substances, excipients, dosage, package, price_from, currency, package_unit_price, warnings}'
```

**Directions and complete medical instructions appear only with `--full`.** Full output adds every published instruction section, including composition, indications, contraindications, interactions, adverse effects, storage, and directions, plus the site's product schema and visible product text. Available sections vary by product. Paragraphs, lists, and table cell boundaries are preserved; full instruction tables and links are also returned separately.

Daily-cost calculations belong outside the CLI. Choose the intended substance and its explicitly declared amount, verify the serving and package units, then calculate `price_from × target_amount_per_day × serving_quantity / (package_quantity × declared_amount)`. Use the unrounded pack price. The result is a normalized daily cost; whole tablets/capsules may not supply exactly that target. The CLI does not choose a daily dose.

Ingredients and dosage remain available as source text. The CLI does not infer elemental amounts, convert compound mass into an active constituent, classify additives as safe/unsafe, rank medicines, or generate advice. Missing information stays absent, and missing composition is reported in `warnings`. The retailer's schema can describe a supplement as a “Drug”; it is not reliable proof of registration status. Confirm important facts against the actual pack/leaflet.

The site uses slugged product URLs. Search automatically remembers each ID's canonical URL in `os.UserCacheDir()/planeta/products/<city>/`. Product facts are fetched fresh by `id`; this cache stores only the URL mapping. It works across working directories and keeps cities separate. Related package URLs are remembered too.

For an ID not previously returned by search, supply its canonical URL:

```sh
planeta id --url "https://planetazdorovo.ru/kazan/catalog/...-15484411/" 15484411
```

Replace the abbreviated example with the real product URL. Unknown IDs fail locally with guidance to search first. If a product URL changes, repeat search.

## Fixed HTTP request count

| Command | Successful requests | What is fetched |
| --- | ---: | --- |
| `search`, any page or result count | 2 | City page + one search page |
| `id`, with or without `--full` | 2 | City page + one product page |
| `auth import`, `auth status`, help | 0 | Local state only |

Failures stop early and make **at most two** requests. The client enforces this budget. It disables retries and redirects, including transport reuse that can trigger implicit retries. No command fetches additional products, pages, images, PDFs, or pharmacy-location AJAX data. Pharmacy availability and “from” prices come from the page; individual pharmacy prices are not fetched.

Put any iteration outside the CLI. For example, in fish:

```fish
planeta search магний хелат > search.json
for id in (jq -r '.results[].id' search.json)
    planeta id $id
end
```

The 15-result example costs 2 search requests plus 15 × 2 product requests. Add `--full` to each product invocation when you need the medical instructions.

Flags can appear before or after positional arguments. A literal `--` ends option parsing. `--timeout` defaults to 90 seconds for the entire command. `--user-agent` can match the importing browser if necessary. Success emits one JSON object to stdout; errors go to stderr and exit nonzero.

For compatibility, `--cookie-file` / `PLANETA_COOKIE_FILE` can still override imported cookies with a legacy Cookie-header file. If no managed auth store exists, `~/.config/planeta/cookies` and then `.planeta-cookies` are fallback sources. Managed imports take precedence over those legacy defaults.

## Verification

```sh
go test -race ./...
go vet ./...
```

Offline tests cover recorded public HTML, request counts, redirects/retries, region selection, pagination, empty searches, negative batch IDs, pharmacy-count extraction (including unknown versus zero and conflicting counts), ingredient and table parsing, default versus full output, browser-import filtering, secret-free auth output, private file permissions, and persistent URL lookup.

Fixtures contain sanitized public catalog markup and synthetic authentication values. Cookies, account data, browser profiles, and personal recommendations are not included. Catalog prices, counts, availability, and labels can change.

To install from a local checkout, run `go install ./cmd/planeta`. The command lives in `cmd/planeta`, so the installed executable is always named `planeta`, independent of the repository name. To build without installing, run `go build -o planeta ./cmd/planeta`.

## License

[MIT](LICENSE).
