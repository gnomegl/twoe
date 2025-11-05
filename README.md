[![Go Version](https://img.shields.io/badge/go-1.24.4-blue.svg)](https://go.dev/)

A command-line tool to fetch tweets using Twitter's public oEmbed API. Designed to be minimal, efficient, and easy to use.

## Features

* Fetches tweets in parallel using multiple goroutines
* Supports reading tweet IDs from a file or discovering them by username
* Writes results to CSV file or outputs NDJSON stream
* Handles errors and retries failed requests
* Optional progress bar display
* All non-data output goes to stderr for easy piping

## Installation

1. Ensure you have Go (1.24.4 or later) installed on your system.
2. Clone this repository or download the `twoe.go` file.
3. Run `go build twoe.go` to build the executable.
4. Run `./twoe -h` to see usage instructions.

## Usage

### File Mode (Fetch from a list of tweet IDs)

```bash
twoe file [options] <tweet_ids_file>

Options:
  -a, --append      Append to existing output file
  -o, --output      Output CSV file (default "tweets_output.csv")
  -p, --parallel    Number of parallel requests (default 50)
  -q, --quiet       Suppress progress bar
  -j, --json        Output NDJSON to stdout instead of CSV
```

### User Mode (Discover and fetch tweets by username)

```bash
twoe user [options] <username>

Options:
  -a, --append      Append to existing output file
  -o, --output      Output CSV file (default "<username>_tweets.csv")
  -p, --parallel    Number of parallel requests (default 50)
  -q, --quiet       Suppress progress bar
  -j, --json        Output NDJSON to stdout instead of CSV
  -c, --check       Only return the count of tweet IDs discovered
```

## Examples

### Fetch tweets from a file to CSV
```bash
./twoe file -o tweets.csv -p 10 tweet_ids.txt
```

### Fetch all tweets by a user to CSV
```bash
./twoe user elonmusk -o elon_tweets.csv
```

### Stream tweets as NDJSON
```bash
./twoe file -j tweet_ids.txt > tweets.ndjson
./twoe user jack -j | jq '.text'  # Process with jq
```

### Check tweet count for a user
```bash
./twoe user elonmusk -c
```

## Output Formats

### CSV Format
The CSV output contains the following columns:
- Name, Followers, Id, Date, Type, Post, URL, Languages, Reposts, Likes, Quotes

Note: Some fields (Followers, Date, Languages, Reposts, Likes, Quotes) are not available from the oEmbed API and are populated with default values.

### NDJSON Format (with `-j/--json` flag)
Each line is a JSON object with the following structure:
```json
{"handle":"username","tweet_id":"123456","type":"Post","text":"tweet content","url":"https://x.com/username/status/123456"}
```

All progress and error messages are sent to stderr, making stdout clean for piping to other tools.

## Notes

* This tool uses Twitter's public oEmbed API, which has usage limits. Be mindful of these limits when fetching large numbers of tweets.
* The `user` command uses the Internet Archive's CDX API to discover tweet IDs, which may also have rate limits.
* This tool is designed to be minimal and has no third-party dependencies beyond the Go standard library.
