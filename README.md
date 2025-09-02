[![Go Version](https://img.shields.io/badge/go-1.24.4-blue.svg)](https://go.dev/)

A command-line tool to fetch tweets using Twitter's public oEmbed API. Designed to be minimal, efficient, and easy to use.

## Features

* Fetches tweets in parallel using multiple goroutines
* Supports reading tweet IDs from a file
* Writes results to a CSV file incrementally
* Handles errors and retries failed requests
* Optional progress bar display

## Installation

1. Ensure you have Go (1.24.4 or later) installed on your system.
2. Clone this repository or download the `twoe.go` file.
3. Run `go build twoe.go` to build the executable.
4. Run `./twoe -h` to see usage instructions.

## Usage

```bash
Usage: twoe [options] <tweet_ids_file>

Twitter/X tweet fetcher using public oEmbed API

Arguments:
  tweet_ids_file    File containing tweet IDs (one per line)

Options:
  -a, --append      Append to existing output file
  -o, --output      Output CSV file (default "tweets_output.csv")
  -p, --parallel    Number of parallel requests (default 20)
  -q, --quiet       Suppress progress bar
```

## Example

```bash
./twoe -o tweets.csv -p 10 tweet_ids.txt
```

## Notes

* This tool uses Twitter's public oEmbed API, which has usage limits. Be mindful of these limits when fetching large numbers of tweets.
* The output CSV file contains the tweet handle, ID, type, text, and URL. Some fields (e.g., followers, date) are not available from the oEmbed API and are populated with default values.
* This tool is designed to be minimal and has no third-party dependencies beyond the Go standard library.
