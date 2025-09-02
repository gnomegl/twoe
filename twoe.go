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

    if !config.Quiet {
        fmt.Printf("Fetching %d tweets...\n", len(tweetIDs))
    }

    // Fetch tweets
    results := fetchTweets(ctx, tweetIDs, config.Parallel, config.Quiet)

    // Write results
    if err := writeResults(config.OutputFile, results); err != nil {
        return fmt.Errorf("failed to write results: %w", err)
    }

    fmt.Printf("✓ Complete! Fetched %d tweets. Results saved to %s\n", len(tweetIDs), config.OutputFile)
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

func fetchTweets(ctx context.Context, tweetIDs []string, parallel int, quiet bool) []TweetResult {
    sem := make(chan struct{}, parallel)
    results := make(chan TweetResult, len(tweetIDs))
    var wg sync.WaitGroup

    // Progress tracking
    var completed int64
    var mu sync.Mutex
    total := len(tweetIDs)

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
                results <- result

                mu.Lock()
                completed++
                mu.Unlock()
            }
        }(tweetID)
    }

    // Close results channel when all workers are done
    go func() {
        wg.Wait()
        close(results)
    }()

    // Collect results
    var allResults []TweetResult
    for result := range results {
        allResults = append(allResults, result)
    }

    if !quiet {
        showProgress(total, total)
        fmt.Println()
    }

    return allResults
}

func fetchTweet(ctx context.Context, tweetID string) TweetResult {
    url := fmt.Sprintf("https://publish.twitter.com/oembed?url=https://twitter.com/i/status/%s", tweetID)

    client := &http.Client{
        Timeout: 10 * time.Second,
        Transport: &http.Transport{
            DisableKeepAlives: false,
        },
    }

    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return createErrorResult(tweetID)
    }

    resp, err := client.Do(req)
    if err != nil {
        return createErrorResult(tweetID)
    }
    defer resp.Body.Close()

    if resp.StatusCode != 200 {
        return createErrorResult(tweetID)
    }

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return createErrorResult(tweetID)
    }

    var tweetData TweetData
    if err := json.Unmarshal(body, &tweetData); err != nil {
        return createErrorResult(tweetID)
    }

    if tweetData.AuthorName == "" {
        return createErrorResult(tweetID)
    }

    return processTweetData(tweetID, tweetData)
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

func writeResults(filename string, results []TweetResult) error {
    file, err := os.OpenFile(filename, os.O_WRONLY|os.O_APPEND, 0644)
    if err != nil {
        return err
    }
    defer file.Close()

    writer := csv.NewWriter(file)
    defer writer.Flush()

    for _, result := range results {
        record := []string{
            "@" + result.Handle,
            "0",
            result.TweetID,
            "2025-08-28 12:00:00",
            result.TweetType,
            result.Text,
            result.URL,
            "en",
            "0",
            "0",
            "0",
        }

        if err := writer.Write(record); err != nil {
            return err
        }
    }

    return nil
}

func showProgress(current, total int) {
    const width = 20
    percentage := current * 100 / total
    filled := current * width / total
    empty := width - filled

    bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
    fmt.Printf("\r\033[K[%s] %d/%d (%d%%)", bar, current, total, percentage)
}
