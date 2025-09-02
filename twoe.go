package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	TweetIDsFile string
	OutputFile   string
	Parallel     int
	Quiet        bool
	Append       bool
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

func main() {
	config := parseFlags()

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		cancel()
	}()

	if err := run(ctx, config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() Config {
	var config Config

	flag.StringVar(&config.OutputFile, "output", "tweets_output.csv", "Output CSV file")
	flag.StringVar(&config.OutputFile, "o", "tweets_output.csv", "Output CSV file")
	flag.IntVar(&config.Parallel, "parallel", 20, "Number of parallel requests")
	flag.IntVar(&config.Parallel, "p", 20, "Number of parallel requests")
	flag.BoolVar(&config.Quiet, "quiet", false, "Suppress progress bar")
	flag.BoolVar(&config.Quiet, "q", false, "Suppress progress bar")
	flag.BoolVar(&config.Append, "append", false, "Append to existing output file")
	flag.BoolVar(&config.Append, "a", false, "Append to existing output file")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <tweet_ids_file>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nTwitter/X tweet fetcher using public oEmbed API\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  tweet_ids_file    File containing tweet IDs (one per line)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}

	config.TweetIDsFile = flag.Arg(0)

	if config.Parallel <= 0 {
		config.Parallel = 20
	}

	return config
}

func run(ctx context.Context, config Config) error {
	// Read tweet IDs
	tweetIDs, err := readTweetIDs(config.TweetIDsFile)
	if err != nil {
		return fmt.Errorf("failed to read tweet IDs: %w", err)
	}

	if len(tweetIDs) == 0 {
		return fmt.Errorf("no tweet IDs found in %s", config.TweetIDsFile)
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
		fmt.Printf("Fetching %d tweets...\n", len(tweetIDs))
	}

	// Fetch tweets and write incrementally
	completed := fetchTweets(ctx, tweetIDs, config.Parallel, config.Quiet, writer)

	fmt.Printf("✓ Complete! Fetched %d tweets. Results saved to %s\n", completed, config.OutputFile)
	return nil
}

func readTweetIDs(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var tweetIDs []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			tweetIDs = append(tweetIDs, line)
		}
	}

	return tweetIDs, scanner.Err()
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
