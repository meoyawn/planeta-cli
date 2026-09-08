package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

const usage = `Usage:
  planeta search [--city kazan] [--page 1] <query>
  planeta id [--full] [--city kazan] [--url URL] <id>
  planeta auth import --browser <browser> [--browser-profile PROFILE]
  planeta auth status

Search fetches one page. ID fetches one product; --full includes all instructions.
Search and id: exactly 2 HTTP requests on success, at most 2 on failure.
Auth and help: 0 HTTP requests. No redirects, retries, or automatic pagination.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "planeta:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command; %s", usage)
	}
	switch args[0] {
	case "--help", "-h", "help":
		_, err := fmt.Fprint(stdout, usage)
		return err
	case "auth":
		return runAuth(ctx, args[1:], stdout, stderr)
	case "search", "id":
	default:
		return fmt.Errorf("unknown command %q; %s", args[0], usage)
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	city := flags.String("city", "kazan", "city URL slug")
	cookieFile := flags.String("cookie-file", os.Getenv("PLANETA_COOKIE_FILE"), "optional legacy Cookie header file; normally use auth import")
	timeout := flags.Duration("timeout", 90*time.Second, "timeout for the entire command")
	userAgent := flags.String("user-agent", defaultUserAgent, "HTTP User-Agent")
	page, full, sourceURL := 1, false, ""
	if command == "search" {
		flags.IntVar(&page, "page", 1, "one result page to fetch (no automatic pagination)")
	} else {
		flags.BoolVar(&full, "full", false, "include all medical instruction sections and source schema")
		flags.StringVar(&sourceURL, "url", "", "canonical product URL when the ID has not been seen in search")
	}
	if err := parseOptions(flags, args[1:], command == "id"); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %q must be positive", timeout.String())
	}
	paths, err := defaultPaths()
	if err != nil {
		return err
	}
	query, id := "", ""
	if command == "search" {
		query = strings.TrimSpace(strings.Join(flags.Args(), " "))
		if query == "" {
			return fmt.Errorf("query %q must not be empty; %s", query, usage)
		}
	} else {
		if flags.NArg() != 1 {
			return fmt.Errorf("id expects one product ID, got %d arguments; %s", flags.NArg(), usage)
		}
		id = flags.Arg(0)
		if !validID.MatchString(id) {
			return fmt.Errorf("invalid product id %q: expected a numeric catalog ID", id)
		}
		if sourceURL == "" {
			sourceURL, err = paths.lookup(*city, id)
			if err != nil {
				return err
			}
		}
	}
	cookies, err := loadClientCookies(*cookieFile, paths.auth, paths.legacy)
	if err != nil {
		return err
	}
	client, err := newClient(siteOrigin, *userAgent, cookies)
	if err != nil {
		return err
	}
	defer client.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if command == "search" {
		result, err := client.Search(ctx, query, *city, page)
		if err != nil {
			return err
		}
		if err := paths.remember(*city, result.Results); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	result, err := client.Detail(ctx, id, *city, sourceURL)
	if err != nil {
		return err
	}
	items := append([]product{result.product}, result.Variants...)
	if err := paths.remember(*city, items); err != nil {
		return err
	}
	if !full {
		result.concise()
	}
	return writeJSON(stdout, result)
}

func runAuth(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("auth expects import or status; %s", usage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(stdout, usage)
		return err
	}
	switch args[0] {
	case "import":
		flags := flag.NewFlagSet("auth import", flag.ContinueOnError)
		flags.SetOutput(stderr)
		browserName := flags.String("browser", "", "browser to import from, e.g. chrome, brave, edge, firefox, safari")
		profile := flags.String("browser-profile", "", "browser profile name/directory; browser default when omitted")
		if err := parseOptions(flags, args[1:], false); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected auth import arguments: %q", flags.Args())
		}
		browser, err := NewBrowserFromValue(*browserName)
		if err != nil {
			return err
		}
		paths, err := defaultPaths()
		if err != nil {
			return err
		}
		result, err := importBrowserCookies(ctx, browser, *profile, paths.auth, sweetcookie.Get)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("auth status accepts no arguments")
		}
		paths, err := defaultPaths()
		if err != nil {
			return err
		}
		store, err := loadAuth(paths.auth)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no browser cookies imported; run planeta auth import --browser chrome (or your browser)")
		}
		if err != nil {
			return err
		}
		result := store.summary(paths.auth)
		if result.ClearanceExpiresAt != nil && !result.ClearanceExpiresAt.After(time.Now()) {
			result.Warnings = append(result.Warnings, "Browser clearance has expired; refresh the site in your browser and run auth import again.")
		}
		return writeJSON(stdout, result)
	default:
		return fmt.Errorf("unknown auth command %q; expected import or status", args[0])
	}
}

// Accept flags on either side of an ID/query. A literal -- ends flag processing;
// negative catalog batch IDs remain positional without requiring --.
func parseOptions(flags *flag.FlagSet, args []string, allowNegativeID bool) error {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" || (allowNegativeID && validID.MatchString(arg)) {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := flags.Lookup(name)
		if option != nil && !hasValue {
			boolFlag, isBool := option.Value.(interface{ IsBoolFlag() bool })
			if !isBool || !boolFlag.IsBoolFlag() {
				if i+1 < len(args) {
					i++
					options = append(options, args[i])
				} else {
					return fmt.Errorf("flag %s requires a value", arg)
				}
			}
		}
	}
	return flags.Parse(append(append(options, "--"), positional...))
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}
