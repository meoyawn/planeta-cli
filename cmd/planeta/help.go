package main

import (
	"bytes"
	"flag"
	"fmt"
)

const usage = `Usage: planeta <command>
From the repository root: go run ./cmd/planeta <command> [options]

Commands:
  auth    Import browser clearance or inspect saved authentication
  config  Show preferences or set the search city
  search  Search products, one page at a time
  id      Show product details, ingredients, prices, and availability

Setup:
  go run ./cmd/planeta auth import --browser chrome
  go run ./cmd/planeta config set city <city-slug>
  go run ./cmd/planeta search 'магний хелат'

Run planeta <command> --help for usage, examples, and options.
For nested commands, run planeta <command> <subcommand> --help.
Success prints JSON to stdout; errors go to stderr and exit nonzero.
Search and id: at most 2 HTTP requests, no retries or automatic pagination.
Matching saved city selection reduces search and id to 1 HTTP request.
Auth, config, and help: 0 HTTP requests. No browser automation.
`

const authUsage = `Usage: planeta auth <command>

Commands:
  import  Copy anonymous site-verification cookies from a browser
  status  Inspect saved cookie names, expiry, and store location

Run planeta auth <command> --help for usage.
`

const configUsage = `Usage:
  planeta config
  planeta config set city <city-slug>

Print saved preferences as JSON, or save the city for search and id commands.
Use the city URL segment from https://planetazdorovo.ru/<city-slug>/.
City starts unset; --city overrides the saved value for one invocation.
Saved slugs are trimmed and lowercased. The only preference is city.
Preferences live in os.UserConfigDir()/planeta/config.json beside auth.json.
These commands are local and do not check whether the city exists on the site.

Example:
  planeta config set city '<city-slug>'
`

func printCommandUsage(flags *flag.FlagSet) error {
	var syntax, details string
	switch flags.Name() {
	case "search":
		syntax = "search <query> [options]"
		details = `Search the selected city's catalog and print one page as JSON.
Set a city with planeta config set city <city-slug>, or pass --city.
Use the city URL segment from https://planetazdorovo.ru/<city-slug>/.
There is no default city. Query words are joined with spaces.

results contains IDs, canonical URLs, names, manufacturer, prices, and pharmacy_counts.
total is all matches; count is this page's size.
Continue the same query and city with --page using next_page when has_more is true.
Search saves product URLs by city so planeta id can fetch fresh details.
Negative IDs identify discounted batches and remain separate products.
pharmacy_counts counts pharmacies, not packs; null means unknown, not zero.
In-stock and orderable groups may overlap; do not add their counts.

Examples:
  planeta search 'магний хелат'
  planeta search 'магний' --page 2
  planeta search 'магний & B6' --city '<city-slug>'`
	case "id":
		syntax = "id <id> [options]"
		details = `Fetch fresh product details and print JSON for the saved city or --city.
Use an ID returned by search; negative discounted-batch IDs are accepted.
If the ID is unknown in this city, search first or supply its canonical --url.
Replace abbreviated URL examples with the real product URL; repeat search if it changes.
Only URLs are cached, in os.UserCacheDir()/planeta/products/<city>/.

Default output includes full ingredients, active_substances, excipients, dosage,
composition_amounts and composition_tables, package, package_unit_price,
price_from, availability, pharmacy_counts, warnings, and related package variants.
--full adds directions, all published medical instructions, and source schema/text.
Published instruction_links are included without fetching PDFs or extra pages.
Amounts preserve declared serving bases; the CLI does not infer elemental amounts
or choose doses. Missing composition is flagged in warnings; check source label text.
price_from is a minimum price; pharmacy_counts counts pharmacies, not packs.
Null counts mean unknown; in-stock and orderable groups may overlap.
id is the canonical URL ID; catalog_id reports a differing internal SKU when present.

Examples:
  planeta id 15484411
  planeta id 15484411 --full
  planeta id -157028
  planeta id 15484411 --url 'https://planetazdorovo.ru/<city-slug>/catalog/...-15484411/'`
	case "auth import":
		syntax = "auth import --browser BROWSER [options]"
		details = `Copy anonymous Planeta cookies from the browser's local store.
Open https://planetazdorovo.ru/ in that browser until the catalog appears first.
No pharmacy account or sign-in is needed. Import does not launch a browser.
Private-window cookies cannot be imported; macOS may show a Keychain prompt.

For Chrome profiles, copy Profile Path from chrome://version into --browser-profile;
the browser's friendly profile name can differ from its on-disk directory.
Cookies are saved privately in os.UserConfigDir()/planeta/auth.json.
Output includes cookie names, expiry, and location, never values or account cookies.
If authentication fails, rerun import and retry the command. If import fails,
reload the site in that browser and rerun import; the CLI does not wait for refreshes.
Failed imports preserve the previous store. Import makes 0 HTTP requests.

Examples:
  planeta auth import --browser chrome
  planeta auth import --browser firefox
  planeta auth import --browser chrome --browser-profile '/path/to/Profile 1'`
	case "auth status":
		syntax = "auth status"
		details = `Print saved browser metadata, cookie names, expiry, and store location as JSON.
This is a local inspection: no browser reads, HTTP requests, or live verification.
Missing imports fail; expired clearance produces a warning to rerun auth import.

Example:
  planeta auth status`
	default:
		panic(fmt.Sprintf("missing help for command %q", flags.Name()))
	}

	var help bytes.Buffer
	fmt.Fprintf(&help, "Usage: planeta %s\n\n%s\n", syntax, details)
	hasOptions := false
	flags.VisitAll(func(*flag.Flag) { hasOptions = true })
	if hasOptions {
		fmt.Fprintln(&help, "\nOptions:")
		output := flags.Output()
		flags.SetOutput(&help)
		flags.PrintDefaults()
		flags.SetOutput(output)
	}
	if flags.Name() == "search" || flags.Name() == "id" {
		fmt.Fprintln(&help, "\n1 HTTP request with matching saved city cookies; otherwise city page + data page (at most 2). No redirects or retries.")
		fmt.Fprintln(&help, "Authentication failure: run planeta auth import --browser <browser>, then retry.")
		fmt.Fprintln(&help, "Legacy cookies: --cookie-file or PLANETA_COOKIE_FILE overrides imported cookies.")
	}
	if hasOptions {
		fmt.Fprintln(&help, "\nFlags may appear before or after arguments; -- ends option parsing.")
	}
	_, err := flags.Output().Write(help.Bytes())
	return err
}
