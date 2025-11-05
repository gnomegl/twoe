package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	TweetIDs   []string
	OutputFile string
	Parallel   int
	Quiet      bool
	Append     bool
}

type TweetData struct {
	AuthorName string `json:"author_name"`
	AuthorURL  string `json:"author_url"`
	HTML       string `json:"html"`
}

type TweetResult struct {
	Handle    string
	TweetID   string
	TweetType string
	Text      string
	URL       string
}

type CDXResult struct {
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
}

func main() {
	if len(os.Args) < 2 {
		printMainUsage()
		os.Exit(1)
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		cancel()
	}()

	var err error
	switch os.Args[1] {
	case "file":
		err = runFileCommand(ctx, os.Args[2:])
	case "user":
		err = runUserCommand(ctx, os.Args[2:])
	case "help", "-h", "--help":
		printMainUsage()
		os.Exit(0)
	default:
		// If no subcommand, treat as file for backwards compatibility
		if !strings.HasPrefix(os.Args[1], "-") && !strings.Contains(os.Args[1], "=") {
			err = runFileCommand(ctx, os.Args[1:])
		} else {
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
			printMainUsage()
			os.Exit(1)
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printMainUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <command> [options]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nTwitter/X tweet fetcher using public oEmbed API\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  file <tweet_ids_file>    Fetch tweets from a file of IDs\n")
	fmt.Fprintf(os.Stderr, "  user <username>          Fetch tweets for a user\n")
	fmt.Fprintf(os.Stderr, "  help                     Show this help message\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  %s file tweet_ids.txt\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s user elonmusk\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s user jack -o jack_tweets.csv\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nFor command-specific help:\n")
	fmt.Fprintf(os.Stderr, "  %s file -h\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s user -h\n", os.Args[0])
}

func runFileCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("file", flag.ExitOnError)

	var config Config
	fs.StringVar(&config.OutputFile, "output", "tweets_output.csv", "Output CSV file")
	fs.StringVar(&config.OutputFile, "o", "tweets_output.csv", "Output CSV file")
	fs.IntVar(&config.Parallel, "parallel", 20, "Number of parallel requests")
	fs.IntVar(&config.Parallel, "p", 20, "Number of parallel requests")
	fs.BoolVar(&config.Quiet, "quiet", false, "Suppress progress bar")
	fs.BoolVar(&config.Quiet, "q", false, "Suppress progress bar")
	fs.BoolVar(&config.Append, "append", false, "Append to existing output file")
	fs.BoolVar(&config.Append, "a", false, "Append to existing output file")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s file [options] <tweet_ids_file>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Fetch tweets from a file containing tweet IDs (one per line)\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  tweet_ids_file    File containing tweet IDs\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(1)
	}

	tweetIDsFile := fs.Arg(0)
	tweetIDs, err := readTweetIDs(tweetIDsFile)
	if err != nil {
		return fmt.Errorf("failed to read tweet IDs: %w", err)
	}

	if len(tweetIDs) == 0 {
		return fmt.Errorf("no tweet IDs found in %s", tweetIDsFile)
	}

	config.TweetIDs = tweetIDs
	return processTweets(ctx, config)
}

func runUserCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("user", flag.ExitOnError)

	var config Config
	fs.StringVar(&config.OutputFile, "output", "", "Output CSV file (default: <username>_tweets.csv)")
	fs.StringVar(&config.OutputFile, "o", "", "Output CSV file (default: <username>_tweets.csv)")
	fs.IntVar(&config.Parallel, "parallel", 20, "Number of parallel requests")
	fs.IntVar(&config.Parallel, "p", 20, "Number of parallel requests")
	fs.BoolVar(&config.Quiet, "quiet", false, "Suppress progress bar")
	fs.BoolVar(&config.Quiet, "q", false, "Suppress progress bar")
	fs.BoolVar(&config.Append, "append", false, "Append to existing output file")
	fs.BoolVar(&config.Append, "a", false, "Append to existing output file")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s user [options] <username>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Fetch tweets for a user\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  username    Twitter username (without @)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(1)
	}

	username := strings.TrimPrefix(fs.Arg(0), "@")

	// Set default output file if not specified
	if config.OutputFile == "" {
		config.OutputFile = fmt.Sprintf("%s_tweets.csv", username)
	}

	// Fetch tweet IDs 
	if !config.Quiet {
		fmt.Printf("Querying for tweets by @%s...\n", username)
	}

	tweetIDs, err := fetchTweetIDsFromCDX(ctx, username, config.Quiet)
	if err != nil {
		return fmt.Errorf("failed to fetch tweet IDs: %w", err)
	}

	if len(tweetIDs) == 0 {
		return fmt.Errorf("no tweets found for @%s", username)
	}

	if !config.Quiet {
		fmt.Printf("Found %d unique tweet IDs for @%s\n", len(tweetIDs), username)
	}

	config.TweetIDs = tweetIDs
	return processTweets(ctx, config)
}

func fetchTweetIDsFromCDX(ctx context.Context, username string, quiet bool) ([]string, error) {
	// Normalize username to lowercase for matching
	usernameLower := strings.ToLower(username)

	// Build CDX API URL
	cdxURL := fmt.Sprintf("https://web.archive.org/cdx/search/cdx?url=twitter.com/%s/status/*&output=json&fl=original&collapse=urlkey", username)

	if !quiet {
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", cdxURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Tweets query failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Query returned status %d", resp.StatusCode)
	}

	// Read entire response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse as JSON array of arrays
	var results [][]string
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("empty response")
	}

	// Skip header (first element)
	if len(results) <= 1 {
		return nil, fmt.Errorf("no data in response")
	}

	tweetIDMap := make(map[string]bool)

	// Regular expression to extract tweet ID from URL
	tweetIDRegex := regexp.MustCompile(`(?i)twitter\.com/([^/]+)/status(?:es)?/(\d+)`)

	// Process each result (skip header at index 0)
	for i := 1; i < len(results); i++ {
		if len(results[i]) == 0 {
			continue
		}

		urlStr := results[i][0]

		// The original field usually doesn't need decoding, but try anyway for safety
		decodedURL, err := url.QueryUnescape(urlStr)
		if err != nil {
			decodedURL = urlStr // Use original if decode fails
		}

		// Extract username and tweet ID from URL
		matches := tweetIDRegex.FindStringSubmatch(decodedURL)
		if len(matches) >= 3 {
			urlUsername := strings.ToLower(matches[1])
			tweetID := matches[2]

			// Only include tweets where the username matches exactly (case-insensitive)
			if urlUsername == usernameLower {
				tweetIDMap[tweetID] = true
			}
		}

		// Show progress for large result sets
		if !quiet && i%1000 == 0 {
			fmt.Printf("\rProcessing results... %d entries", i)
		}
	}

	if !quiet && len(results) > 1000 {
		fmt.Printf("\r\033[K") // Clear the progress line
	}

	// Convert map to slice
	tweetIDs := make([]string, 0, len(tweetIDMap))
	for id := range tweetIDMap {
		tweetIDs = append(tweetIDs, id)
	}

	return tweetIDs, nil
}

func processTweets(ctx context.Context, config Config) error {
	if len(config.TweetIDs) == 0 {
		return fmt.Errorf("no tweet IDs to process")
	}

	// Initialize output file
	if err := initOutputFile(config.OutputFile, config.Append); err != nil {
		return fmt.Errorf("failed to initialize output file: %w", err)
	}

	// Open file for incremental writing
	file, err := os.OpenFile(config.OutputFile, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open output file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	if !config.Quiet {
		fmt.Printf("Fetching %d tweets...\n", len(config.TweetIDs))
	}

	// Fetch tweets and write incrementally
	completed := fetchTweets(ctx, config.TweetIDs, config.Parallel, config.Quiet, writer)

	fmt.Printf("✓ Complete! Fetched %d tweets. Results saved to %s\n", completed, config.OutputFile)
	return nil
}

func readTweetIDs(filename string) ([]string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	// Try to handle different encodings - clean invalid UTF-8 sequences
	content := string(data)

	// Split by newlines and process
	var tweetIDs []string
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		// Clean up the line - remove invalid UTF-8 and whitespace
		line = strings.TrimSpace(line)
		// Remove any non-printable characters
		cleaned := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\t' {
				return ' '
			}
			if r < 32 && r != '\n' {
				return -1 // Remove control characters
			}
			if r == 0xFFFD { // Unicode replacement character
				return -1
			}
			return r
		}, line)

		cleaned = strings.TrimSpace(cleaned)
		if cleaned != "" {
			tweetIDs = append(tweetIDs, cleaned)
		}
	}

	return tweetIDs, nil
}

func initOutputFile(filename string, append bool) error {
	// Check if file exists
	_, err := os.Stat(filename)
	fileExists := err == nil

	if !append || !fileExists {
		// Create new file with header
		file, err := os.Create(filename)
		if err != nil {
			return err
		}
		defer file.Close()

		writer := csv.NewWriter(file)
		defer writer.Flush()

		header := []string{"Name", "Followers", "Id", "Date", "Type", "Post", "URL", "Languages", "Reposts", "Likes", "Quotes"}
		return writer.Write(header)
	}

	return nil
}

func fetchTweets(ctx context.Context, tweetIDs []string, parallel int, quiet bool, writer *csv.Writer) int {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	// Progress tracking
	var completed int64
	var successCount int64
	var mu sync.Mutex
	total := len(tweetIDs)

	// Mutex for CSV writer
	var writerMu sync.Mutex

	if !quiet {
		showProgress(0, total)
	}

	// Progress updater
	if !quiet {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					mu.Lock()
					current := int(completed)
					mu.Unlock()
					showProgress(current, total)
					if current >= total {
						return
					}
				}
			}
		}()
	}

	// Track failed tweets for reporting
	var failedTweets []string
	var failedMu sync.Mutex

	// Launch workers
	for _, tweetID := range tweetIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()

			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()

				result := fetchTweet(ctx, id)

				// Write result immediately if successful
				if result.Text != "Tweet not found" {
					record := []string{
						"@" + result.Handle,
						"0", // Followers - not available from oEmbed
						result.TweetID,
						"2025-08-28 12:00:00", // Date - not available from oEmbed
						result.TweetType,
						result.Text,
						result.URL,
						"en", // Language - not available from oEmbed
						"0",  // Reposts - not available from oEmbed
						"0",  // Likes - not available from oEmbed
						"0",  // Quotes - not available from oEmbed
					}

					writerMu.Lock()
					writer.Write(record)
					writer.Flush() // Flush immediately to write to disk
					writerMu.Unlock()

					mu.Lock()
					successCount++
					mu.Unlock()
				} else {
					// Track failed tweet
					failedMu.Lock()
					failedTweets = append(failedTweets, id)
					failedMu.Unlock()
				}

				mu.Lock()
				completed++
				mu.Unlock()
			}
		}(tweetID)
	}

	// Wait for all workers to complete
	wg.Wait()

	if !quiet {
		showProgress(total, total)
		fmt.Println()
	}

	// Report failed tweets if any
	if len(failedTweets) > 0 {
		fmt.Printf("\nFailed to fetch %d tweets (after retries):\n", len(failedTweets))
		// Show first 10 failed IDs as examples
		maxShow := 10
		if len(failedTweets) < maxShow {
			maxShow = len(failedTweets)
		}
		for i := 0; i < maxShow; i++ {
			fmt.Printf("  - %s\n", failedTweets[i])
		}
		if len(failedTweets) > maxShow {
			fmt.Printf("  ... and %d more\n", len(failedTweets)-maxShow)
		}
	}

	return int(successCount)
}

func fetchTweet(ctx context.Context, tweetID string) TweetResult {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return createErrorResult(tweetID)
			case <-time.After(backoff):
			}
		}

		result, err := attemptFetchTweet(ctx, tweetID)
		if err == nil {
			return result
		}
		lastErr = err
	}

	// All retries failed
	if lastErr != nil {
		// Log the error for debugging (optional)
		// fmt.Fprintf(os.Stderr, "Failed to fetch tweet %s after %d attempts: %v\n", tweetID, maxRetries, lastErr)
	}
	return createErrorResult(tweetID)
}

func attemptFetchTweet(ctx context.Context, tweetID string) (TweetResult, error) {
	url := fmt.Sprintf("https://publish.twitter.com/oembed?url=https://twitter.com/i/status/%s", tweetID)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: false,
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return TweetResult{}, fmt.Errorf("request creation failed: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return TweetResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		// Tweet doesn't exist, no point retrying
		return createErrorResult(tweetID), nil
	}

	if resp.StatusCode != 200 {
		return TweetResult{}, fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return TweetResult{}, fmt.Errorf("failed to read body: %w", err)
	}

	var tweetData TweetData
	if err := json.Unmarshal(body, &tweetData); err != nil {
		return TweetResult{}, fmt.Errorf("failed to parse JSON: %w", err)
	}

	if tweetData.AuthorName == "" {
		return TweetResult{}, fmt.Errorf("empty author name")
	}

	return processTweetData(tweetID, tweetData), nil
}

func processTweetData(tweetID string, data TweetData) TweetResult {
	// Extract handle from author URL
	handle := "unknown"
	if data.AuthorURL != "" {
		re := regexp.MustCompile(`twitter\.com/([^/]+)`)
		matches := re.FindStringSubmatch(data.AuthorURL)
		if len(matches) > 1 {
			handle = matches[1]
		}
	}

	// Clean HTML content
	text := cleanHTML(data.HTML)

	// Determine tweet type
	tweetType := "Post"
	if strings.HasPrefix(text, "@") {
		tweetType = "Replies"
	}

	return TweetResult{
		Handle:    handle,
		TweetID:   tweetID,
		TweetType: tweetType,
		Text:      text,
		URL:       fmt.Sprintf("https://x.com/%s/status/%s", handle, tweetID),
	}
}

func cleanHTML(html string) string {
	// Remove HTML tags
	re := regexp.MustCompile(`<[^>]*>`)
	text := re.ReplaceAllString(html, "")

	// Decode HTML entities
	replacements := map[string]string{
		"&lt;":   "<",
		"&gt;":   ">",
		"&amp;":  "&",
		"&quot;": "\"",
	}

	for old, new := range replacements {
		text = strings.ReplaceAll(text, old, new)
	}

	// Remove Twitter pic links and author attribution
	re = regexp.MustCompile(`pic\.twitter\.com\S*`)
	text = re.ReplaceAllString(text, "")

	re = regexp.MustCompile(`—.*`)
	text = re.ReplaceAllString(text, "")

	// Clean whitespace
	text = strings.TrimSpace(text)
	re = regexp.MustCompile(`\s+`)
	text = re.ReplaceAllString(text, " ")

	// Limit length
	if len(text) > 200 {
		text = text[:200]
	}

	return text
}

func createErrorResult(tweetID string) TweetResult {
	return TweetResult{
		Handle:    "unknown",
		TweetID:   tweetID,
		TweetType: "Post",
		Text:      "Tweet not found",
		URL:       fmt.Sprintf("https://x.com/i/status/%s", tweetID),
	}
}

func showProgress(current, total int) {
	const width = 20
	percentage := current * 100 / total
	filled := current * width / total
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	fmt.Printf("\r\033[K[%s] %d/%d (%d%%)", bar, current, total, percentage)
}
