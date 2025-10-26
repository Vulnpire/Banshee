# Banshee

is a high‑speed Google Programmable Search Engine (CSE) powered dorking tool for security reconnaissance and OSINT. It automates refined Google queries to quickly uncover exposed files, hidden paths, sensitive content, and subdomains across a target’s web footprint — all while handling API quotas, pagination, and rate limiting for you.

Built for practitioners who need actionable results fast, Banshee leverages Google’s search quality with focused, customizable dorks to surface what matters.


- Files by extension (e.g., pdf, xlsx, bak)
- Interesting paths/keywords in URLs (inurl:)
- Text/content within pages (intext:)
- Subdomains
- Arbitrary custom dorks

It supports:
- Multiple API keys with automatic rotation and quota handling
- Optional proxy
- Exclusions
- Domain lists
- Graceful Ctrl+C shutdown
- Output to file with de-duplication

Version: 1.33.7

## Why Banshee?

- Passive reconnaissance: Reduce direct interaction with target infrastructure by relying on Google’s indexed view.
- Precision at scale: Automatically generate and paginate structured dorks across multiple site scopes (*.domain, *.*.domain, etc.).
- Wordlist‑driven discovery: Feed in a dictionary of filenames/paths (e.g., admin.php, backup.zip, .git, /api/) and let inurl queries expose indexed traces.
- Robust execution: Key rotation, adaptive delays, domain batching, and graceful cancellation keep long hunts smooth and resilient.
- Clean output: Link filtering and de‑duplication cut noise and keep results practical.

## Quick start

1) Install Go 1.20+  
2) Install:

```bash
go install -v github.com/Vulnpire/Banshee@latest
```

3) Configure Google Custom Search (CSE) and API keys (see below).  
4) Drop your API keys into:  
   - Linux/macOS: ~/.config/banshee/keys.txt  
   - One key per line.

5) Run:

```bash
./banshee -u example.com -e pdf,docx -p 2
```


## Google Custom Search setup (CSE + API key)

Banshee uses Google Custom Search JSON API. You need:
- A Google Cloud API key with the Custom Search API enabled
- A Programmable Search Engine (CSE) ID (cx)

Follow these steps:

1) Create a Google Cloud project
- https://console.cloud.google.com/
- Create a project (or use an existing one)

2) Enable Custom Search API
- In the project, go to APIs & Services -> Library
- Search for “Custom Search API”
- Enable it

3) Create an API Key
- APIs & Services -> Credentials -> Create Credentials -> API key
- Copy the key; you’ll add it to keys.txt

4) Create a Programmable Search Engine (CSE)
- https://programmablesearchengine.google.com/
- Add a new search engine
- You can either:
  - Add a placeholder site (e.g., example.com) and later allow searching the entire web, or
  - Directly set it to search the entire web
- In Search engine -> Setup -> Basics, make sure “Search the entire web” is enabled if available (UI may vary by account/region)
- Copy the Search engine ID (cx)

5) Set your CX in the code or via fork
- The current code uses:
  - defaultAPIURL = https://www.googleapis.com/customsearch/v1
  - defaultCX = "759aed2f7b4be4b83" (example placeholder)
- Replace defaultCX with your own CSE ID if needed and rebuild, or fork with your CX.

6) Add API keys file
- Create directory: ~/.config/banshee/ - `mkdir -p ~/.config/banshee`
- Create file: ~/.config/banshee/keys.txt - `vi ~/.config/banshee/keys.txt`
- Put one API key per line; Banshee rotates among them:
  ```
  AIzaSyExampleKey1
  AIzaSyExampleKey2
  ```
- Keep an eye on quota usage. When one key hits quota, Banshee will try others.


## Usage

Run without args to see help:

```bash
./banshee -h
```

Flags:
- -h, --help: Display help

<img width="765" height="860" alt="image" src="https://github.com/user-attachments/assets/9073f044-cbf0-4455-8fc6-8a99df8370e4" />

- -u, --url <TARGET>: Domain or IP to target (required unless using -f)

<img width="447" height="295" alt="image" src="https://github.com/user-attachments/assets/a4ff44cb-5efa-41f4-8500-299a950494d0" />

- -f, --file <FILENAME>: File with one domain per line
- -e, --extensions <EXT>: Comma-separated list or file with extensions

<img width="430" height="62" alt="image" src="https://github.com/user-attachments/assets/85591d81-4688-49fa-9806-aa888e0f2caa" />

- -w, --word <DICTIONARY>: Comma-separated list or file of paths/keywords for inurl: searches

<img width="1106" height="219" alt="image" src="https://github.com/user-attachments/assets/3383b816-93b1-4638-abeb-a0b1c7ed5cac" />

- -c, --contents <TEXT>: Comma-separated list or file for intext: searches

<img width="1313" height="149" alt="image" src="https://github.com/user-attachments/assets/0bdfb381-61ba-41aa-8872-ccb3239550c7" />

- -q, --query <QUERY>: Custom query (full control)

<img width="1551" height="222" alt="image" src="https://github.com/user-attachments/assets/0d43920c-5cf1-40c6-9f06-a149de79b940" />

- -s, --subdomains: Subdomain discovery for the target

<img width="403" height="324" alt="image" src="https://github.com/user-attachments/assets/913bed2c-d45f-4f5c-a47f-1fb6f9cf01ee" />

- -a, --recursive: Include subdomains in queries (aggressive mode)

<img width="1180" height="678" alt="image" src="https://github.com/user-attachments/assets/6f601e47-ced1-434f-aba7-6af2ec5e0333" />
 
- -x, --exclusions <EXCLUSIONS>: Comma-separated list or file of sites to exclude
- -p, --pages <PAGES>: Number of pages to paginate through (default 10)
- -d, --delay <SECONDS>: Static delay between requests (otherwise adaptive)
- -o, --output <FILE>: Write results (deduplicated) to file
- -r, --proxy <PROXY>: Proxy, e.g., http://127.0.0.1:8080
- -v, --verbose: Verbose logging

Examples:
- Search for multiple extensions on a domain:
  ```bash
  ./banshee -u example.com -e pdf,doc,bak
  ```
- Search extension with pagination:
  ```bash
  ./banshee -u example.com -e pdf -p 2
  ```
- Load extensions from file and include subdomains:
  ```bash
  ./banshee -u example.com -e extensions.txt -a
  ```
- inurl dictionary search:
  ```bash
  ./banshee -u example.com -w config.php,admin,/images/
  ./banshee -u example.com -w wordlist.txt
  ```
- inurl with exclusions:
  ```bash
  ./banshee -u example.com -w login.html,search,redirect,?id= -x admin.example.com
  ./banshee -u example.com -w admin.html,search,redirect,?id= -x exclusion_list.txt
  ```
- Subdomains:
  ```bash
  ./banshee -u example.com -s -p 10 -d 5 -o subdomains.txt
  ```
- intext searches:
  ```bash
  ./banshee -u example.com -c Passport,Password,Confidential,Secret
  ```
- Use a proxy:
  ```bash
  ./banshee -u example.com -r http://proxy.example.com:8080
  ```
- Custom dork:
  ```bash
  ./banshee -u example.com -q 'ext:jsp' -a
  ```
- Bulk domains:
  ```bash
  ./banshee -f domains.txt -w wordlist.txt
  ```

## How it works

- Builds Google CSE queries using:
  - site: scopes (domain, *.domain, *.*.domain, etc.)
  - ext: and filetype: for extensions
  - inurl:"term" for dictionary mode
  - intext:"term" for content mode
  - Optional exclusions via -x (translated to -site: patterns)
- Fetches JSON results from the Custom Search API and extracts links
- Filters non-target and Google-owned links
- De-duplicates, prints or writes to file (append-only unique)
- Handles pagination and adaptive rate limiting
- Rotates API keys and marks exhausted keys
- Gracefully shuts down on Ctrl+C:
  - First Ctrl+C: cancels context and finishes in-flight operations, printing partial results
  - Second Ctrl+C: forces exit (code 130)

## Operational guidance

- Passive by design: Results come from Google’s index. This minimizes direct touch on targets compared to active crawlers.
- Wordlist strategy: Provide a dictionary of paths or filenames; Banshee will apply targeted inurl queries to surface indexed paths that resemble crawler findings—without directly crawling the site.
- Scope control: Combine -a with -x exclusions to broaden scope while keeping noise down.
- Output hygiene: Use -o to accumulate a clean, deduplicated corpus across runs.
- Quota strategy: Maintain several API keys in keys.txt; reduce pages or increase delay for long sessions.

## Tips

- Quota: The CSE JSON API has daily and per-second quotas. Add multiple keys to keys.txt for longer runs.
- CX: Ensure your CSE is allowed to search the entire web. If limited to specific sites, results may be sparse.
- Exclusions: Use -x to cut noise (e.g., docs, blogs, cdn subdomains).
- Output: Use -o to persist results and avoid duplicates across runs.

## Axiom Support

```
[
        {
                "command":"/home/op/go/bin/banshee --url input --delay 5 | anew output",
                "ext":"txt",
                "threads":"3"
        }
]
```

## Troubleshooting

- No results for an extension:
  - Try adding -a to include subdomains
  - Ensure your CSE is set to search the entire web
  - Try both ext and filetype by using -e; Banshee issues both patterns automatically
- Quota exceeded:
  - Add more API keys to keys.txt
  - Increase -d (delay) or let the adaptive delay work; reduce -p
- Proxy errors:
  - Verify protocol scheme (http:// or socks5:// if supported by your environment)
- Force exit:
  - Press Ctrl+C twice


## Security, ethics, and responsibility

Banshee is intended for legitimate security testing and research. Use it only on systems and domains you own or are explicitly authorized to test.

You are solely responsible for how you use this tool, for complying with all applicable laws, regulations, and third‑party terms (including Google’s API terms), and for respecting target scopes and permissions. The authors and contributors disclaim liability for misuse, damage, or violations arising from the use of Banshee. Proceed ethically and responsibly.


## License

MIT License
