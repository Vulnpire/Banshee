package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultAPIURL   = "https://www.googleapis.com/customsearch/v1"
	defaultCX       = "759aed2f7b4be4b83"
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 GLS/100.10.9939.100"
	version         = "1.33.7"
)

type GoogleResponse struct {
	Items []struct {
		Link string `json:"link"`
	} `json:"items"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type Config struct {
	// Inputs and flags
	target            string
	pages             int
	dork              string
	exclusions        string
	contents          string
	delay             float64
	dictionary        string
	extension         string
	outputPath        string
	domainsFile       string
	proxy             string
	includeSubdomains bool
	subdomainMode     bool // set when -s used
	verbose           bool

	// Derived
	excludeTargets string
	inFile         string
	inUrl          string

	// Keys
	apiKeys        []string
	exhaustedKeys  map[string]struct{}

	// HTTP / runtime
	client       *http.Client
	dynamicDelay float64
	requestStore []string

	// internal flags
	resultsFound     bool
	requestCounter   int
	noResultCounter  int
}

func main() {
	cfg := &Config{
		exhaustedKeys: make(map[string]struct{}),
		dynamicDelay:  0.25,
	}

	// Flags
	help := flag.Bool("h", false, "Display help")
	flag.BoolVar(help, "help", *help, "Display help")

	flag.StringVar(&cfg.domainsFile, "f", "", "Specify a file containing domains to target")
	flag.StringVar(&cfg.domainsFile, "file", "", "Specify a file containing domains to target")

	flag.BoolVar(&cfg.subdomainMode, "s", false, "Lists subdomains of the specified domain")
	flag.BoolVar(&cfg.subdomainMode, "subdomains", false, "Lists subdomains of the specified domain")

	flag.BoolVar(&cfg.includeSubdomains, "a", false, "Aggressive crawling (subdomains included)")
	flag.BoolVar(&cfg.includeSubdomains, "recursive", false, "Aggressive crawling (subdomains included)")

	flag.IntVar(&cfg.pages, "p", 0, "Specify the number of pages")
	flag.IntVar(&cfg.pages, "pages", 0, "Specify the number of pages")

	flag.StringVar(&cfg.dork, "q", "", "Specify a query string")
	flag.StringVar(&cfg.dork, "query", "", "Specify a query string")

	flag.StringVar(&cfg.exclusions, "x", "", "Excludes targets in searches (comma-separated or file)")
	flag.StringVar(&cfg.exclusions, "exclusions", "", "Excludes targets in searches (comma-separated or file)")

	flag.StringVar(&cfg.contents, "c", "", "Specify relevant content in comma-separated files or file path")
	flag.StringVar(&cfg.contents, "contents", "", "Specify relevant content in comma-separated files or file path")

	flag.Float64Var(&cfg.delay, "d", 0, "Delay in seconds between requests")
	flag.Float64Var(&cfg.delay, "delay", 0, "Delay in seconds between requests")

	flag.StringVar(&cfg.dictionary, "w", "", "Specify a DICTIONARY/paths/files (comma-separated or file)")
	flag.StringVar(&cfg.dictionary, "word", "", "Specify a DICTIONARY/paths/files (comma-separated or file)")

	flag.StringVar(&cfg.extension, "e", "", "Specify comma-separated extensions or file")
	flag.StringVar(&cfg.extension, "extensions", "", "Specify comma-separated extensions or file")

	flag.StringVar(&cfg.outputPath, "o", "", "Export the results to a file (results only)")
	flag.StringVar(&cfg.outputPath, "output", "", "Export the results to a file (results only)")

	flag.StringVar(&cfg.target, "u", "", "Specify a DOMAIN or IP Address")
	flag.StringVar(&cfg.target, "url", "", "Specify a DOMAIN or IP Address")

	flag.StringVar(&cfg.proxy, "r", "", "Specify an [protocol://]host[:port] proxy")
	flag.StringVar(&cfg.proxy, "proxy", "", "Specify an [protocol://]host[:port] proxy")

	flag.BoolVar(&cfg.verbose, "v", false, "Enable verbose")
	flag.BoolVar(&cfg.verbose, "verbose", false, "Enable verbose")

	flag.Parse()

	if *help {
		showBanner()
		printUsage()
		return
	}

	// Graceful Ctrl+C handling: first signal -> cancel context; second signal -> hard exit
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		count := 0
		for sig := range sigCh {
			count++
			if count == 1 {
				logErr("[!] Caught %s, attempting graceful shutdown... (press Ctrl+C again to force)", sig.String())
				cancel()
			} else {
				logErr("[!] Force exiting.")
				os.Exit(130)
			}
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
		cancel()
	}()

	// HTTP client with optional proxy
	cl, err := buildHTTPClient(cfg.proxy)
	if err != nil {
		logErr("[!] Invalid proxy: %v", err)
		os.Exit(1)
	}
	cfg.client = cl

	// Load API keys...
	if err := cfg.loadAPIKeysDefault(); err != nil {
		logErr("keys.txt not found or unreadable: %v", err)
		os.Exit(1)
	}

	// Preprocess helpers...
	if cfg.exclusions != "" {
		cfg.excludeTargets = buildExclusions(cfg.exclusions, cfg.includeSubdomains)
	}
	if cfg.contents != "" {
		cfg.inFile = buildContentsQuery(cfg.contents)
	}
	if cfg.dictionary != "" {
		cfg.inUrl = buildInurlQuery(cfg.dictionary)
	}

	// Domains file flow
	if cfg.domainsFile != "" {
		if err := cfg.readDomainsFile(ctx); err != nil {
			// If context was canceled, exit quietly with code 130
			if errors.Is(err, context.Canceled) {
				os.Exit(130)
			}
			logErr("%v", err)
			os.Exit(1)
		}
		return
	}

	// Single target flow
	if cfg.target == "" {
		showErrorAndExit()
	}

	var ran bool
	if cfg.target != "" && cfg.dictionary != "" {
		ran = true
		cfg.dictionaryAttack(ctx)
	}
	if cfg.target != "" && cfg.extension != "" {
		ran = true
		cfg.extensionAttack(ctx)
	}
	if cfg.target != "" && cfg.subdomainMode {
		ran = true
		cfg.subdomainAttack(ctx)
	}
	if cfg.target != "" && cfg.contents != "" {
		ran = true
		cfg.contentsAttack(ctx)
	}
	if cfg.target != "" && cfg.dork != "" {
		ran = true
		res := cfg.dorkRun(ctx, "")
		if len(res) == 0 {
			// If cancelled, exit with 130; otherwise, normal notFound behavior
			if ctx.Err() != nil {
				os.Exit(130)
			}
			cfg.notFound()
		} else {
			outputOrPrintUnique(res, cfg.outputPath)
		}
	}
	if !ran {
		showErrorAndExit()
	}
}

func showBanner() {
	// ASCII banner;
	fmt.Println("⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⡄⠀⠀⠀⠀⠀")
	fmt.Println("⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⣀⣇⡀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀")
	fmt.Println("⠀⠀⠀⠀⠀⠀⢀⣠⠴⠚⠋⠉⢹⣯⠉⠉⠛⠲⢤⣀⠀⠀⠀⠀⠀⠀")
	fmt.Println("⠀⠀⠀⠀⢀⡴⠋⠀⣣⠤⠒⠒⣺⣿⡒⠒⠢⢤⡃⠉⠳⣄⠀⠀⠀⠀")
	fmt.Println("⢀⡀⠀⣠⠋⠀⡰⠊⠀⠀⢀⣼⠏⠈⢿⣄⠀⠀⠈⠲⡀⠈⢧⡀⠀⣀")
	fmt.Println("⠀⠈⢳⠧⢤⣞⠀⠀⠀⢀⣾⠏⠀⠀⠈⢿⣆⠀⠀⠀⢘⣦⠤⢷⠋⠀")
	fmt.Println("⠀⠀⡾⠤⡼⠈⠛⢦⣤⣾⡏⣠⠶⠲⢦⡈⣿⣦⣤⠞⠋⢹⡤⠼⡇⠀")
	fmt.Println("⠀⠀⡇⠀⠆⠀⠀⢾⣿⣿⢸⡁⣾⣿⠆⣻⢸⣿⢾⠄⠀⠀⠆⠀⡇⠀")
	fmt.Println("⠀⠀⣧⠐⢲⠀⣠⡼⠟⣿⡆⠳⢬⣥⠴⢃⣿⡟⠻⣤⡀⢸⠒⢠⡇⠀")
	fmt.Println("⠀⢀⣸⡤⠞⢏⠁⠀⠀⠘⢿⡄⠀⠀⠀⣼⠟⠀⠀⠀⢙⠟⠦⣼⣀⠀")
	fmt.Println("⠐⠉⠀⠹⡄⠈⠣⡀⠀⠀⠈⢿⣄⢀⣾⠏⠀⠀⠀⡠⠋⢀⡼⠁⠈⠑")
	fmt.Println("⠀⠀⠀⠀⠙⢦⣀⠈⡗⠢⠤⢈⣻⣿⣃⠠⠤⠲⡍⢀⣠⠞⠀⠀⠀⠀")
	fmt.Println("⠀⠀⠀⠀⠀⠀⠈⠛⠦⣄⣀⡀⢸⣏⠀⣀⣀⡤⠞⠋⠀⠀⠀⠀⠀⠀")
	fmt.Println("⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠉⠉⠉⡏⠉⠉⠁⠀⠀⠀⠀⠀⠀⠀⠀⠀")
	fmt.Println("⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠇⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀")
	fmt.Printf(" Banshee v%s\n - Made by Vulnpire\n\n", version)
}

func printUsage() {
	fmt.Println(`Usage:
    -h|--help                                Display this help message.
    -a|--recursive                 Aggressive crawling (subdomains included).
    -w|--word <DICTIONARY>        Specify a DICTIONARY, PATHS or FILES.
    -e|--extensions <EXTENSION>           Specify comma-separated extensions.
    -u|--url <TARGET>                  Specify a DOMAIN or IP Address.
    -p|--pages <PAGES>                      Specify the number of PAGES.
    -x|--exclusions <EXCLUSIONS>                EXCLUDES targets in searches.
    -d|--delay <DELAY>                Delay in seconds between requests.
    -s|--subdomains                 Lists subdomains of the specified domain.
    -c|--contents <TEXT> Specify relevant content in comma-separated files.
    -o|--output <FILENAME>   Export the results to a file (results only).
    -r|--proxy <PROXY>        Specify an [protocol://]host[:port] proxy.
    -f|--file <FILENAME>   Specify a file containing domains to target.
    -q|--query <QUERY>     Specify a query string.
    -v|--verbose      Enable verbose.

Examples:
    banshee -u example.com -e pdf,doc,bak
    banshee -u example.com -e pdf -p 2
    banshee -u example.com -e extensionslist.txt -a
    banshee -u example.com -w config.php,admin,/images/
    banshee -u example.com -w wp-admin -p 1
    banshee -u example.com -w wordlist.txt
    banshee -u example.com -w login.html,search,redirect,?id= -x admin.example.com
    banshee -u example.com -w admin.html,search,redirect,?id= -x exclusion_list.txt
    banshee -u example.com -s -p 10 -d 5 -o banshee-subdomains.txt
    banshee -u example.com -c Passport,Password,Confidential,Secret
    banshee -u example.com -r http://proxy.example.com:8080
    banshee -u example.com -q <query> -a
    banshee -f domains.txt -w wordlist.txt`)
}

func showErrorAndExit() {
	logErr("[!] Error, missing or invalid argument.")
	printUsage()
	os.Exit(1)
}

func logv(v bool, f string, a ...any) {
	if v {
		fmt.Printf(f+"\n", a...)
	}
}

func logErr(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
}

// --- API Keys ---

func (c *Config) loadAPIKeysDefault() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".config", "banshee", "keys.txt")
	return c.readApiKeysFromFile(path)
}

func (c *Config) readApiKeysFromFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var keys []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		keys = append(keys, line)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("no API keys in file")
	}
	c.apiKeys = keys
	return nil
}

func (c *Config) getRandomApiKey() (string, error) {
	available := make([]string, 0, len(c.apiKeys))
	for _, k := range c.apiKeys {
		if _, ex := c.exhaustedKeys[k]; !ex {
			available = append(available, k)
		}
	}
	if len(available) == 0 {
		return "", errors.New("no available API keys left. All keys have exceeded their quota")
	}
	// Rotate pseudo-randomly by time
	idx := int(time.Now().UnixNano()) % len(available)
	return available[idx], nil
}

// --- Query builders ---

func buildExclusions(exclusions string, multiline bool) string {
	// Build "-site:....+ -site:..." contiguous string
	var parts []string
	if fileExists(exclusions) {
		lines, _ := readLines(exclusions)
		for _, ex := range lines {
			ex = strings.TrimSpace(ex)
			if ex == "" {
				continue
			}
			parts = append(parts, ex)
		}
	} else if strings.Contains(exclusions, ",") {
		for _, ex := range strings.Split(exclusions, ",") {
			ex = strings.TrimSpace(ex)
			if ex == "" {
				continue
			}
			parts = append(parts, ex)
		}
	} else {
		parts = append(parts, strings.TrimSpace(exclusions))
	}
	// Reconstruct: first "-site:<ex1>" then "+-<ex2>"…
	// For simplicity, concatenated with "+-" for additional entries
	var b strings.Builder
	if len(parts) > 0 {
		b.WriteString("-site:")
		b.WriteString(parts[0])
		for i := 1; i < len(parts); i++ {
			b.WriteString("+-")
			b.WriteString(parts[i])
		}
	}
	return b.String()
}

func buildContentsQuery(contents string) string {
	// Build intext:"..." OR intext:"a" OR intext:"a" OR intext:"b" style
	// When file: each line becomes its own search later; here we return a single term.
	// For a single value or comma-separated, build using logical OR (|) as Google supports OR.
	if fileExists(contents) {
		// Caller iterates per line; here return placeholder for single value path.
		lines, _ := readLines(contents)
		if len(lines) > 0 {
			// first line
			return fmt.Sprintf(`intext:"%s"`, lines[0])
		}
		return ""
	}
	if strings.Contains(contents, ",") {
		parts := []string{}
		for _, s := range strings.Split(contents, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				parts = append(parts, fmt.Sprintf(`intext:"%s"`, s))
			}
		}
		// Join with OR to broaden results similar to "+||+"
		return strings.Join(parts, " OR ")
	}
	return fmt.Sprintf(`intext:"%s"`, contents)
}

func buildInurlQuery(dict string) string {
	// Return raw terms joined with a sentinel "|||".
	// We will wrap each as inurl:"term" later per request to avoid awkward OR behavior.
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		// avoid wrapping quotes inside the value; strip surrounding quotes if provided
		s = strings.Trim(s, `"`)
		return s
	}

	var terms []string
	if fileExists(dict) {
		lines, _ := readLines(dict)
		for _, s := range lines {
			if t := clean(s); t != "" {
				terms = append(terms, t)
			}
		}
	} else if strings.Contains(dict, ",") {
		for _, s := range strings.Split(dict, ",") {
			if t := clean(s); t != "" {
				terms = append(terms, t)
			}
		}
	} else {
		if t := clean(dict); t != "" {
			terms = append(terms, t)
		}
	}
	return strings.Join(terms, "|||")
}

// --- IO helpers ---

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readLines(p string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s != "" {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}

func outputOrPrintUnique(urls []string, outputPath string) {
	uniq := uniqueStrings(urls)
	sort.Strings(uniq)
	if outputPath == "" {
		for _, u := range uniq {
			fmt.Println(u)
		}
		return
	}
	// emulate "anew": append only new unique lines compared to file
	existing := map[string]struct{}{}
	if fileExists(outputPath) {
		lines, _ := readLines(outputPath)
		for _, l := range lines {
			existing[l] = struct{}{}
		}
	}
	f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logErr("[!] cannot open output file: %v", err)
		// fallback to stdout
		for _, u := range uniq {
			fmt.Println(u)
		}
		return
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	for _, u := range uniq {
		if _, ok := existing[u]; !ok {
			bw.WriteString(u)
			bw.WriteByte('\n')
			existing[u] = struct{}{}
		}
	}
}

// --- HTTP client and requests ---

func buildHTTPClient(proxyURL string) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   20 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          50,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}, nil
}

func (c *Config) httpGetJSON(ctx context.Context, u string) (*GoogleResponse, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var gr GoogleResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		// still return code for troubleshooting
		return nil, resp.StatusCode, fmt.Errorf("decode error: %w, body: %s", err, string(body))
	}
	return &gr, resp.StatusCode, nil
}


func (c *Config) notFound() {
	// HTML redirect check; here API returns JSON errors.
	// keep silent as per commented-out prints.
}

func (c *Config) showContentInFile() {
	// This only prints when contents set; kept minimal
	if c.contents != "" && c.verbose {
		fmt.Printf("Files found containing: %s\n", c.contents)
	}
}

// urlDecode similar to sed
func urlDecodeLikeSed(s string) string {
	// First standard percent-decoding
	decoded, err := url.QueryUnescape(s)
	if err != nil {
		decoded = s
	}
	// Then specific replacements to mimic the sed line (some overlapped)
	repls := map[string]string{
		"%2520": " ",
		"%20":   " ",
		"%3F":   "?",
		"%3D":   "=",
		"%21":   "!",
		"%23":   "#",
		"%24":   "$",
		"%2B":   "+",
		"%26":   "&",
	}
	for k, v := range repls {
		decoded = strings.ReplaceAll(decoded, k, v)
	}
	return decoded
}

var googleHostFilter = regexp.MustCompile(`(?i)google`)

func filterLinks(items []string, target string) []string {
	out := make([]string, 0, len(items))
	for _, l := range items {
		if l == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(l), strings.ToLower(target)) {
			continue
		}
		if googleHostFilter.MatchString(l) {
			continue
		}
		out = append(out, urlDecodeLikeSed(l))
	}
	return uniqueStrings(out)
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (c *Config) delayControl() {
	d := c.dynamicDelay
	if c.delay > 0 {
		d = c.delay
	}
	if d > 0 {
		time.Sleep(time.Duration(d * float64(time.Second)))
	}
}

func (c *Config) readDomainsFile(ctx context.Context) error {
	lines, err := readLines(c.domainsFile)
	if err != nil {
		return fmt.Errorf("[!] Error, file not found: %s", c.domainsFile)
	}
	for _, line := range lines {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		target := strings.TrimSpace(line)
		if target == "" {
			continue
		}
		c2 := *c
		c2.target = target

		if c2.dork != "" {
			res := c2.dorkRun(ctx, "")
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if len(res) == 0 {
				c2.notFound()
			} else {
				outputOrPrintUnique(res, c2.outputPath)
			}
		} else if c2.extension != "" {
			c2.extensionAttack(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if c2.dictionary != "" {
			c2.dictionaryAttack(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if c2.subdomainMode {
			c2.subdomainAttack(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if c2.contents != "" {
			c2.contentsAttack(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return nil
}

// dorkRun is the central querying routine
func (c *Config) dorkRun(ctx context.Context, ext string) []string {
	c.requestStore = nil
	page := 0
	c.requestCounter = 0
	c.noResultCounter = 0
	c.resultsFound = false
	if c.pages == 0 {
		c.pages = 10
	}

	for page < c.pages {
		if ctx.Err() != nil {
			return c.requestStore
		}

		startIdx := page*10 + 1 // CSE is 1-based

		var triedKeys int
		maxTries := len(c.apiKeys)

		for triedKeys < maxTries {
			if ctx.Err() != nil {
				return c.requestStore
			}

			apiKey, err := c.getRandomApiKey()
			if err != nil || apiKey == "" {
				logErr("No valid API keys remaining.")
				return c.requestStore
			}
			logv(c.verbose, "Using API Key: %s", apiKey)

			base := fmt.Sprintf("%s?key=%s&cx=%s&start=%d", defaultAPIURL, url.QueryEscape(apiKey), url.QueryEscape(defaultCX), startIdx)

			buildOne := func(q string) string {
				return base + "&q=" + url.QueryEscape(strings.TrimSpace(q))
			}
			withExcl := func(q string) string {
				if c.excludeTargets != "" {
					q = q + " " + c.excludeTargets
				}
				return q
			}

			var urls []string

			switch {
			case c.dork != "":
				if c.includeSubdomains {
					urls = append(urls,
						buildOne(withExcl(fmt.Sprintf("site:*.%s %s -www.%s", c.target, c.dork, c.target))),
						buildOne(withExcl(fmt.Sprintf("site:*.*.%s %s", c.target, c.dork))),
						buildOne(withExcl(fmt.Sprintf("site:*.*.*.%s %s", c.target, c.dork))),
						buildOne(withExcl(fmt.Sprintf("site:*.%s %s -www.%s -techblog.%s -infohub.%s -blog.%s -store.%s -support.%s -help.%s -addons.%s -forum.%s -community.%s -docs.%s -developer.%s -about.%s -resources.%s -cdn.%s -career.%s -faq.%s -news.%s -jobs.%s -library.%s -id.%s -blogs.%s -faq.%s -trust.%s -forums.%s -dl.%s -downloads.%s",
							c.target, c.dork, c.target,
							c.target, c.target, c.target, c.target, c.target, c.target, c.target, c.target,
							c.target, c.target, c.target, c.target, c.target, c.target, c.target, c.target,
							c.target, c.target, c.target, c.target, c.target, c.target, c.target, c.target, c.target))),
					)
				} else {
					urls = append(urls, buildOne(withExcl(fmt.Sprintf("site:%s %s", c.target, c.dork))))
				}

			case ext != "":
				extToken := strings.TrimSpace(ext)
				buildQ := func(scope string) []string {
					return []string{
						withExcl(fmt.Sprintf(`%s filetype:%s`, scope, extToken)),
						withExcl(fmt.Sprintf(`%s ext:%s`, scope, extToken)),
					}
				}
				if c.includeSubdomains {
					for _, scope := range []string{
						fmt.Sprintf("site:%s", c.target),
						fmt.Sprintf("site:*.%s", c.target),
						fmt.Sprintf("site:*.*.%s", c.target),
						fmt.Sprintf("site:*.*.*.%s", c.target),
					} {
						for _, q := range buildQ(scope) {
							urls = append(urls, buildOne(q))
						}
					}
				} else {
					for _, q := range buildQ(fmt.Sprintf("site:%s", c.target)) {
						urls = append(urls, buildOne(q))
					}
				}

			case c.dictionary != "":
				var terms []string
				if c.inUrl != "" {
					terms = strings.Split(c.inUrl, "|||")
				}
				if len(terms) == 0 {
					terms = []string{c.dictionary}
				}
				buildQ := func(prefix, term string) string {
					q := fmt.Sprintf(`%s inurl:"%s"`, prefix, strings.TrimSpace(term))
					return withExcl(q)
				}
				if c.includeSubdomains {
					for _, t := range terms {
						t = strings.TrimSpace(t)
						if t == "" {
							continue
						}
						urls = append(urls,
							buildOne(buildQ(fmt.Sprintf("site:*.%s", c.target), t)),
							buildOne(buildQ(fmt.Sprintf("site:*.*.%s", c.target), t)),
							buildOne(buildQ(fmt.Sprintf("site:*.*.*.%s", c.target), t)),
						)
					}
				} else {
					for _, t := range terms {
						t = strings.TrimSpace(t)
						if t == "" {
							continue
						}
						urls = append(urls, buildOne(buildQ(fmt.Sprintf("site:%s", c.target), t)))
					}
				}

			case c.contents != "":
				buildQ := func(prefix string) string {
					return withExcl(fmt.Sprintf(`%s %s`, prefix, c.inFile))
				}
				if c.includeSubdomains {
					urls = append(urls,
						buildOne(buildQ(fmt.Sprintf("site:*.%s", c.target))),
						buildOne(buildQ(fmt.Sprintf("site:*.*.%s", c.target))),
						buildOne(buildQ(fmt.Sprintf("site:*.*.*.%s", c.target))),
					)
				} else {
					urls = append(urls, buildOne(buildQ(fmt.Sprintf("site:%s", c.target))))
				}

			default:
				urls = append(urls, buildOne(withExcl(fmt.Sprintf("site:%s", c.target))))
			}

			var combined []string
			var respErr error
			for _, u := range urls {
				if ctx.Err() != nil {
					return c.requestStore
				}
				gr, _, err := c.httpGetJSON(ctx, u)
				if err != nil {
					respErr = err
					continue
				}
				if gr.Error != nil && gr.Error.Message != "" {
					if strings.Contains(strings.ToLower(gr.Error.Message), "quota") {
						c.exhaustedKeys[apiKey] = struct{}{}
					}
					respErr = errors.New(gr.Error.Message)
					continue
				}
				var links []string
				for _, it := range gr.Items {
					links = append(links, it.Link)
				}
				links = filterLinks(links, c.target)
				combined = append(combined, links...)
			}

			combined = uniqueStrings(combined)
			if len(combined) > 0 {
				c.requestStore = append(c.requestStore, combined...)
				c.resultsFound = true
				c.noResultCounter = 0
				c.requestCounter++
				if c.delay == 0 && c.dynamicDelay > 0.05 {
					c.dynamicDelay -= 0.05
				}
				break
			}

			if respErr != nil {
				logv(c.verbose, "Error: %v", respErr)
				triedKeys++
			} else {
				c.delayControl()
				c.noResultCounter++
				triedKeys = maxTries
				if c.delay == 0 {
					c.dynamicDelay += 0.1
				}
			}
			c.delayControl()
		}

		if !c.resultsFound {
			break
		}
		c.resultsFound = false
		page++
	}

	if len(c.requestStore) == 0 {
		c.notFound()
		return nil
	}
	return c.requestStore
}

func (c *Config) dictionaryAttack(ctx context.Context) {
	if c.verbose {
		fmt.Printf("Target: %s\n", c.target)
	}
	if c.inUrl == "" {
		c.inUrl = buildInurlQuery(c.dictionary)
	}
	res := c.dorkRun(ctx, "")
	if len(res) == 0 {
		c.notFound()
		return
	}
	if c.outputPath != "" {
		outputOrPrintUnique(res, c.outputPath)
	} else {
		outputOrPrintUnique(res, "")
	}
}
func (c *Config) extensionAttack(ctx context.Context) {
	var exts []string
	if fileExists(c.extension) {
		lines, _ := readLines(c.extension)
		exts = lines
	} else if strings.Contains(c.extension, ",") {
		for _, t := range strings.Split(c.extension, ",") {
			if s := strings.TrimSpace(t); s != "" {
				exts = append(exts, s)
			}
		}
	} else if c.extension != "" {
		exts = []string{strings.TrimSpace(c.extension)}
	}

	var all []string
	for _, ext := range exts {
		select {
		case <-ctx.Done():
			logErr("Operation cancelled: %v", ctx.Err())
			return
		default:
		}
		if c.verbose {
			fmt.Printf("Checking extension: %s\n", ext)
		}
		res := c.dorkRun(ctx, ext)
		if len(res) > 0 {
			all = append(all, res...)
		}
	}

	if len(all) == 0 {
		c.notFound()
		return
	}
	all = uniqueStrings(all)
	if c.outputPath != "" {
		outputOrPrintUnique(all, c.outputPath)
	} else {
		for _, u := range all {
			fmt.Println(u)
		}
	}
}

func (c *Config) performExtensionRequest(ctx context.Context, ext string) {
	if c.verbose {
		fmt.Printf("Checking extension: %s\n", ext)
	}
	res := c.dorkRun(ctx, ext)
	if len(res) == 0 {
		c.notFound()
		return
	}
	c.showContentInFile()
	if c.outputPath != "" {
		outputOrPrintUnique(res, c.outputPath)
	}
}

func (c *Config) subdomainAttack(ctx context.Context) {
	if c.verbose {
		fmt.Printf("Target: %s\n", c.target)
	}
	res := c.dorkRun(ctx, "")
	if len(res) == 0 {
		c.notFound()
		return
	}
	// Print subdomains (awk -F/ '{print $3}' | sort -u)
	hostSet := map[string]struct{}{}
	for _, u := range res {
		h := hostOf(u)
		if h != "" {
			hostSet[h] = struct{}{}
		}
	}
	hosts := make([]string, 0, len(hostSet))
	for h := range hostSet {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	if c.outputPath != "" {
		outputOrPrintUnique(hosts, c.outputPath)
	} else {
		for _, h := range hosts {
			fmt.Println(h)
		}
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// try add scheme
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			u2, e2 := url.Parse("http://" + raw)
			if e2 == nil && u2.Host != "" {
				return u2.Host
			}
		}
		return ""
	}
	return u.Host
}

func (c *Config) contentsAttack(ctx context.Context) {
	if c.verbose {
		fmt.Printf("Target: %s\n", c.target)
	}
	if fileExists(c.contents) {
		lines, _ := readLines(c.contents)
		for _, content := range lines {
			c2 := *c
			c2.contents = content
			// Build intext for this single term
			c2.inFile = fmt.Sprintf(`intext:"%s"`, content)
			res := c2.dorkRun(ctx, "")
			if len(res) == 0 {
				c2.notFound()
				continue
			}
			if c2.verbose {
				fmt.Printf("Files found containing: %s\n", content)
			}
			if c2.outputPath != "" {
				outputOrPrintUnique(res, c2.outputPath)
			} else {
				outputOrPrintUnique(res, "")
			}
		}
		return
	}
	// Single value path
	c.inFile = buildContentsQuery(c.contents)
	res := c.dorkRun(ctx, "")
	if len(res) == 0 {
		c.notFound()
		return
	}
	if c.outputPath != "" {
		outputOrPrintUnique(res, c.outputPath)
	} else {
		outputOrPrintUnique(res, "")
	}
}

// --- Concurrency-safe unique writer (parallelization for later) ---
type SafeSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func NewSafeSet() *SafeSet {
	return &SafeSet{m: make(map[string]struct{})}
}

func (s *SafeSet) Add(v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[v]; ok {
		return false
	}
	s.m[v] = struct{}{}
	return true
}
