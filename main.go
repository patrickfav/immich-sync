package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Asset struct {
	Title        *string `json:"title"`
	Description  string  `json:"description"`
	Date         struct {
		Timestamp string `json:"timestamp"`
	} `json:"date"`
	CreationTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"creationTime"`
	PhotoTakenTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
}

type ImmichAlbumAsset struct {
	Assets ImmichAssetAssets `json:"assets"`
}

type ImmichAssetAssets struct {
	Total      int           `json:"total"`
	Count      int           `json:"count"`
	Items      []ImmichAsset `json:"items"`
	NextPage   *string       `json:"nextPage"`
	NextCursor *string       `json:"nextCursor"`
}

type ImmichAsset struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	OriginalFileName string `json:"originalFileName"`
	OriginalMimeType string `json:"originalMimeType"`
	FileCreatedAt    string `json:"fileCreatedAt"`
}

type Album struct {
	AlbumName string `json:"albumName"`
	ID        string `json:"id"`
}

type TakeoutFile struct {
	Filename       string
	CreationTime   string
	PhotoTakenTime string
}

type assetSearchQuery struct {
	Filename    string
	TakenAfter  string
	TakenBefore string
}

type assetSearchResult struct {
	Query  assetSearchQuery
	Assets []ImmichAsset
	Err    error
}

// Keep concurrent metadata searches bounded so faster syncs do not overwhelm Immich.
const assetSearchWorkers = 6

const (
	maxRateLimitRetries          = 5
	defaultRateLimitCooldown     = time.Minute
	maxFallbackRateLimitCooldown = 5 * time.Minute
	retryAfterSafetyBuffer       = time.Second
)

func readJSONFile(path string, value any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, value)
}

func getJSONData(path string, method string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return getData(path, method, string(body))
}

func apiEndpoint(path string) (string, error) {
	baseURL := strings.TrimSpace(apiURL)
	if baseURL == "" {
		return "", errors.New("Immich API URL is empty")
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/"), nil
}

func takeoutFilenameFromMetadata(name string) (string, bool) {
	for _, suffix := range []string{".supplemental-metadata.json", ".suppl.json"} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), true
		}
	}

	// Google may disambiguate duplicate sidecars as .suppl(1).json.
	if strings.HasSuffix(name, ".json") {
		base := strings.TrimSuffix(name, ".json")
		if suffixStart := strings.LastIndex(base, ".suppl("); suffixStart >= 0 && strings.HasSuffix(base, ")") {
			return base[:suffixStart], true
		}
	}

	return "", false
}

var takeout map[string][]TakeoutFile

var takeoutAlbumCreationTimes map[string]int64

var takeoutAlbumDescriptions map[string]string

var takeoutFiles []TakeoutFile

var assetSearchCache map[assetSearchQuery][]ImmichAsset

var (
	apiURL      string
	apiKey      string
	takeoutPath string
)

var (
	suppressConfirmation = false
	approveAllAlbums     = false
	stdinReader          = bufio.NewReader(os.Stdin)
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

var rateLimitCooldown struct {
	sync.Mutex
	until time.Time
}

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiCyan   = "\033[36m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiBlue   = "\033[34m"
)

func colorsEnabled() bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
		return false
	}
	return terminalOutputEnabled()
}

func terminalOutputEnabled() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func colorize(color string, text string) string {
	if !colorsEnabled() {
		return text
	}
	return color + text + ansiReset
}

func section(icon string, title string) {
	line := strings.Repeat("─", 56)
	fmt.Printf("\n%s %s\n%s\n", icon, colorize(ansiBold, title), colorize(ansiDim, line))
}

func info(format string, args ...any) {
	fmt.Printf("%s %s\n", colorize(ansiCyan, "ℹ"), fmt.Sprintf(format, args...))
}

func success(format string, args ...any) {
	fmt.Printf("%s %s\n", colorize(ansiGreen, "✓"), fmt.Sprintf(format, args...))
}

func warning(format string, args ...any) {
	fmt.Printf("%s %s\n", colorize(ansiYellow, "⚠"), fmt.Sprintf(format, args...))
}

func failure(format string, args ...any) {
	fmt.Printf("%s %s\n", colorize(ansiRed, "✗"), fmt.Sprintf(format, args...))
}

func progressBar(current int, total int) string {
	const width = 24
	if total <= 0 {
		return strings.Repeat("─", width)
	}
	filled := current * width / total
	if filled > width {
		filled = width
	}
	return strings.Repeat("━", filled) + strings.Repeat("─", width-filled)
}

func startProgress(label string, total int) bool {
	interactive := terminalOutputEnabled() && colorsEnabled()
	if interactive {
		fmt.Printf("%s %s [%s] 0/%d", colorize(ansiBlue, "→"), label, progressBar(0, total), total)
	} else {
		info("%s (%d images)", label, total)
	}
	return interactive
}

func updateProgress(interactive bool, label string, current int, total int) {
	if !interactive {
		return
	}
	fmt.Printf("\r\033[K%s %s [%s] %d/%d", colorize(ansiBlue, "→"), label, progressBar(current, total), current, total)
}

func finishProgress(interactive bool, label string, total int) {
	if interactive {
		updateProgress(true, label, total, total)
		fmt.Printf(" %s\n", colorize(ansiGreen, "✓"))
		return
	}
	success("Checked %d images", total)
}

func stopProgress(interactive bool) {
	if interactive {
		fmt.Println()
	}
}

func promptPrefix() string {
	return colorize(ansiBlue, "?")
}

func waitForRateLimitCooldown() {
	for {
		rateLimitCooldown.Lock()
		wait := time.Until(rateLimitCooldown.until)
		rateLimitCooldown.Unlock()
		if wait <= 0 {
			return
		}
		time.Sleep(wait)
	}
}

func setRateLimitCooldown(delay time.Duration) {
	if delay <= 0 {
		return
	}

	rateLimitCooldown.Lock()
	defer rateLimitCooldown.Unlock()
	until := time.Now().Add(delay)
	if until.After(rateLimitCooldown.until) {
		rateLimitCooldown.until = until
	}
}

func rateLimitDelay(headers http.Header, attempt int, now time.Time) (time.Duration, string) {
	if retryAfter := strings.TrimSpace(headers.Get("Retry-After")); retryAfter != "" {
		if seconds, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && seconds >= 0 {
			return time.Duration(seconds)*time.Second + retryAfterSafetyBuffer, "Retry-After"
		}
		if retryAt, err := http.ParseTime(retryAfter); err == nil {
			delay := retryAt.Sub(now)
			if delay < 0 {
				delay = 0
			}
			return delay + retryAfterSafetyBuffer, "Retry-After"
		}
	}

	if reset := strings.TrimSpace(headers.Get("X-RateLimit-Reset")); reset != "" {
		if seconds, err := strconv.ParseInt(reset, 10, 64); err == nil {
			if delay := time.Unix(seconds, 0).Sub(now); delay > 0 {
				return delay, "X-RateLimit-Reset"
			}
			return 0, "X-RateLimit-Reset"
		}
	}

	delay := defaultRateLimitCooldown
	for range attempt {
		if delay >= maxFallbackRateLimitCooldown/2 {
			return maxFallbackRateLimitCooldown, "fallback"
		}
		delay *= 2
	}
	return delay, "fallback"
}

func getData(path string, method string, payload string) ([]byte, error) {
	if apiKey == "" {
		return nil, errors.New("Immich API key is empty")
	}
	endpoint, err := apiEndpoint(path)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		waitForRateLimitCooldown()

		req, err := http.NewRequest(method, endpoint, strings.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-api-key", apiKey)

		res, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}

		if res.StatusCode == http.StatusTooManyRequests && attempt < maxRateLimitRetries {
			delay, source := rateLimitDelay(res.Header, attempt, time.Now())
			setRateLimitCooldown(delay)
			warning("Immich API rate limit reached; retrying in %s (%d/%d, %s)", delay.Round(time.Second), attempt+1, maxRateLimitRetries, source)
			continue
		}
		if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
			if res.StatusCode == http.StatusTooManyRequests {
				return nil, fmt.Errorf("Immich API %s %s returned HTTP %s after %d retries: %s", method, path, res.Status, maxRateLimitRetries, strings.TrimSpace(string(body)))
			}
			return nil, fmt.Errorf("Immich API %s %s returned HTTP %s: %s", method, path, res.Status, strings.TrimSpace(string(body)))
		}
		return body, nil
	}
}

func YesNoPrompt(label string, def bool, required bool) bool {
	if !required && suppressConfirmation {
		return true
	}
	choices := "Y/n"
	if !def {
		choices = "y/N"
	}

	var s string

	for {
		fmt.Fprintf(os.Stderr, "%s %s (%s) ", promptPrefix(), label, choices)
		line, err := stdinReader.ReadString('\n')
		if err != nil && line == "" {
			return false
		}
		s = line
		s = strings.TrimSpace(s)
		if s == "" {
			return def
		}
		s = strings.ToLower(s)
		if s == "y" || s == "yes" {
			return true
		}
		if s == "n" || s == "no" {
			return false
		}
	}
}

func AlbumConfirmation(label string) (confirm bool, approveAll bool) {
	if suppressConfirmation {
		return true, false
	}

	for {
		fmt.Fprintf(os.Stderr, "%s %s (Y/n/A) ", promptPrefix(), label)
		s, err := stdinReader.ReadString('\n')
		if err != nil && s == "" {
			return false, false
		}
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "", "y", "yes":
			return true, false
		case "a", "all":
			return true, true
		case "n", "no":
			return false, false
		}
	}
}

func confirmAlbumAction(label string) bool {
	if approveAllAlbums {
		return true
	}
	ok, approveAll := AlbumConfirmation(label)
	if approveAll {
		approveAllAlbums = true
	}
	return ok
}

func UserInput(label string, def string, required bool) (string, bool) {
	var s string

	for {
		if def != "" {
			fmt.Fprintf(os.Stderr, "%s %s (default: %s): ", promptPrefix(), label, def)
		} else {
			fmt.Fprintf(os.Stderr, "%s %s: ", promptPrefix(), label)
		}
		line, err := stdinReader.ReadString('\n')
		if err != nil && line == "" {
			return "", false
		}
		s = line
		s = strings.TrimSpace(s)
		if s == "" {
			if def != "" {
				return def, true
			}
			if required {
				continue
			} else {
				return "", true
			}
		}
		return s, true
	}
}

func getAlbums() (albums []Album, err error) {
	section("🗂️", "Immich albums")
	info("Loading albums...")
	body, err := getData("albums", http.MethodGet, "")
	if err != nil {
		failure("%v", err)
		return nil, err
	}
	if err := json.Unmarshal(body, &albums); err != nil {
		return nil, fmt.Errorf("invalid albums response: %w", err)
	}
	success("Found %d albums", len(albums))
	return albums, nil
}

func findAlbumByName(albums []Album, albumName string) (Album, bool) {
	for _, album := range albums {
		if album.AlbumName == albumName {
			return album, true
		}
	}
	return Album{}, false
}

func orderedAlbumNames(albums map[string][]TakeoutFile, creationTimes map[string]int64) []string {
	names := make([]string, 0, len(albums))
	for name := range albums {
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		leftDate, leftHasDate := creationTimes[names[i]]
		rightDate, rightHasDate := creationTimes[names[j]]
		if leftHasDate != rightHasDate {
			return leftHasDate
		}
		if leftHasDate && leftDate != rightDate {
			return leftDate > rightDate
		}
		return names[i] < names[j]
	})

	return names
}

func readFiles(readAll bool) error {
	basePath := takeoutPath
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return fmt.Errorf("could not read Takeout directory %q: %w", basePath, err)
	}
	takeout = make(map[string][]TakeoutFile)
	takeoutAlbumCreationTimes = make(map[string]int64)
	takeoutAlbumDescriptions = make(map[string]string)
	takeoutFiles = nil
	section("📦", "Google Takeout")
	info("Reading album metadata...")
	albumsTotal := 0
	imagesTotal := 0
	for _, entry := range entries {
		entryPath := filepath.Join(basePath, entry.Name())
		if readAll && entry.IsDir() {
			if err := readAllDir(entryPath); err != nil {
				warning("Could not read Takeout directory %s: %v", entryPath, err)
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}

		var asset Asset
		if err := readJSONFile(filepath.Join(entryPath, "metadata.json"), &asset); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				warning("Could not read album metadata %s: %v", entryPath, err)
			}
			continue
		}
		if asset.Title != nil && *asset.Title != "" {
			albumsTotal++
			takeoutAlbumDescriptions[*asset.Title] = strings.TrimSpace(asset.Description)
			if timestamp, err := strconv.ParseInt(asset.Date.Timestamp, 10, 64); err == nil {
				takeoutAlbumCreationTimes[*asset.Title] = timestamp
			}
			if err := readAlbum(entryPath, *asset.Title); err != nil {
				warning("Could not read album directory %s: %v", entryPath, err)
				continue
			}
			imagesTotal += len(takeout[*asset.Title])
		}
	}
	if albumsTotal == 0 {
		warning("No albums found")
	} else {
		success("Found %d albums with %d images", albumsTotal, imagesTotal)
	}
	return nil
}

func readAlbum(albumPath string, albumTitle string) error {
	entries, err := os.ReadDir(albumPath)
	if err != nil {
		return err
	}
	if _, ok := takeout[albumTitle]; !ok {
		takeout[albumTitle] = []TakeoutFile{}
	}
	for _, entry := range entries {
		if entry.Name() == "metadata.json" {
			continue
		}
		fileName, ok := takeoutFilenameFromMetadata(entry.Name())
		if !ok {
			continue
		}
		var asset Asset
		if err := readJSONFile(filepath.Join(albumPath, entry.Name()), &asset); err != nil {
			warning("Could not read photo metadata %s: %v", entry.Name(), err)
			continue
		}
		if asset.Title != nil && *asset.Title != "" {
			fileName = *asset.Title
		}
		if fileName != "" {
			takeout[albumTitle] = append(takeout[albumTitle], TakeoutFile{Filename: fileName, CreationTime: asset.CreationTime.Timestamp, PhotoTakenTime: asset.PhotoTakenTime.Timestamp})
		}
	}
	imageCount := len(takeout[albumTitle])
	fmt.Printf("  %s %-*s %s\n", colorize(ansiBlue, "▸"), 48, albumTitle, colorize(ansiDim, fmt.Sprintf("%d images", imageCount)))
	return nil
}

func readAllDir(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			if err := readAllDir(entryPath); err != nil {
				return err
			}
		} else {
			fileName, ok := takeoutFilenameFromMetadata(entry.Name())
			if !ok {
				continue
			}
			var asset Asset
			if err := readJSONFile(entryPath, &asset); err != nil {
				warning("Could not read photo metadata %s: %v", entryPath, err)
				continue
			}
			if asset.PhotoTakenTime.Timestamp != "" {
				takeoutFiles = append(takeoutFiles, TakeoutFile{Filename: fileName, CreationTime: asset.CreationTime.Timestamp, PhotoTakenTime: asset.PhotoTakenTime.Timestamp})
			}
		}
	}
	return nil
}

func searchMetadata(payload any) (ImmichAssetAssets, error) {
	body, err := getJSONData("search/metadata", http.MethodPost, payload)
	if err != nil {
		return ImmichAssetAssets{}, err
	}
	var response ImmichAlbumAsset
	if err := json.Unmarshal(body, &response); err != nil {
		return ImmichAssetAssets{}, fmt.Errorf("invalid Immich search response: %w", err)
	}
	return response.Assets, nil
}

func searchAssetsByFilename(query assetSearchQuery) ([]ImmichAsset, error) {
	payload := struct {
		OriginalFileName string `json:"originalFileName"`
		TakenAfter       string `json:"takenAfter,omitempty"`
		TakenBefore      string `json:"takenBefore,omitempty"`
		Page             int    `json:"page"`
		Size             int    `json:"size"`
	}{OriginalFileName: query.Filename, TakenAfter: query.TakenAfter, TakenBefore: query.TakenBefore, Page: 1, Size: 1000}
	var assets []ImmichAsset
	for {
		response, err := searchMetadata(payload)
		if err != nil {
			return nil, err
		}
		assets = append(assets, response.Items...)
		if response.NextPage == nil || *response.NextPage == "" {
			return assets, nil
		}
		nextPage, err := strconv.Atoi(*response.NextPage)
		if err != nil {
			return nil, fmt.Errorf("invalid nextPage in Immich filename search response: %w", err)
		}
		if nextPage <= payload.Page {
			return nil, fmt.Errorf("invalid nextPage %d in Immich filename search response", nextPage)
		}
		payload.Page = nextPage
	}
}

func findAssetByFilename(filename string, createdAfter string, createdBefore string) ([]ImmichAsset, error) {
	return searchAssetsByFilename(assetSearchQuery{
		Filename:    filename,
		TakenAfter:  createdAfter,
		TakenBefore: createdBefore,
	})
}

func searchAssetQueries(queries []assetSearchQuery, workerCount int, progress func(int)) (map[assetSearchQuery][]ImmichAsset, error) {
	resultsByQuery := make(map[assetSearchQuery][]ImmichAsset, len(queries))
	if len(queries) == 0 {
		return resultsByQuery, nil
	}
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(queries) {
		workerCount = len(queries)
	}

	jobs := make(chan assetSearchQuery)
	results := make(chan assetSearchResult, len(queries))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for query := range jobs {
				assets, err := searchAssetsByFilename(query)
				results <- assetSearchResult{Query: query, Assets: assets, Err: err}
			}
		}()
	}

	go func() {
		for _, query := range queries {
			jobs <- query
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()

	var firstErr error
	completed := 0
	for result := range results {
		completed++
		if progress != nil {
			progress(completed)
		}
		if result.Err != nil {
			if firstErr == nil {
				firstErr = result.Err
			}
			continue
		}
		resultsByQuery[result.Query] = result.Assets
	}

	return resultsByQuery, firstErr
}

func getAlbumAssets(albumID string) ([]ImmichAsset, error) {
	var assets []ImmichAsset
	page := 1

	for {
		payload := struct {
			AlbumIDs []string `json:"albumIds"`
			Page     int      `json:"page"`
			Size     int      `json:"size"`
		}{AlbumIDs: []string{albumID}, Page: page, Size: 1000}

		response, err := searchMetadata(payload)
		if err != nil {
			return nil, err
		}
		assets = append(assets, response.Items...)

		if response.NextPage == nil || *response.NextPage == "" {
			return assets, nil
		}
		nextPage, err := strconv.Atoi(*response.NextPage)
		if err != nil {
			return nil, fmt.Errorf("invalid nextPage in Immich album assets response: %w", err)
		}
		if nextPage <= page {
			return nil, fmt.Errorf("invalid nextPage %d in Immich album assets response", nextPage)
		}
		page = nextPage
	}
}

func findAssetsByDate(date string) (assets []ImmichAsset, err error) {
	parsedDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q: expected YYYY-MM-DD", date)
	}
	payload := struct {
		TakenAfter  string `json:"takenAfter"`
		TakenBefore string `json:"takenBefore"`
		Page        int    `json:"page"`
		Size        int    `json:"size"`
	}{TakenAfter: parsedDate.UTC().Format("2006-01-02T00:00:00Z"), TakenBefore: parsedDate.UTC().Format("2006-01-02T23:59:59Z"), Page: 1, Size: 1000}

	for {
		response, searchErr := searchMetadata(payload)
		if searchErr != nil {
			return nil, searchErr
		}
		assets = append(assets, response.Items...)
		if response.NextPage == nil || *response.NextPage == "" {
			return assets, nil
		}
		nextPage, parseErr := strconv.Atoi(*response.NextPage)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid nextPage in Immich date search response: %w", parseErr)
		}
		if nextPage <= payload.Page {
			return nil, fmt.Errorf("invalid nextPage %d in Immich date search response", nextPage)
		}
		payload.Page = nextPage
	}
}

func takeoutPhotoTime(file TakeoutFile) (time.Time, error) {
	timestamp, err := strconv.ParseInt(file.PhotoTakenTime, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid Takeout photo timestamp %q: %w", file.PhotoTakenTime, err)
	}
	return time.Unix(timestamp, 0), nil
}

func parseTakeoutTimestamp(primary string, fallback string) (int64, error) {
	if primary != "" {
		if timestamp, err := strconv.ParseInt(primary, 10, 64); err == nil {
			return timestamp, nil
		}
	}
	if fallback != "" {
		if timestamp, err := strconv.ParseInt(fallback, 10, 64); err == nil {
			return timestamp, nil
		}
	}
	return 0, fmt.Errorf("timestamps %q and %q are not valid Unix timestamps", primary, fallback)
}

func syncAssetDate(file TakeoutFile, asset ImmichAsset) {
	tm, err := takeoutPhotoTime(file)
	if err != nil {
		warning("Skipping %s: %v", file.Filename, err)
		return
	}
	assetCreatedAt, err := time.Parse(time.RFC3339Nano, asset.FileCreatedAt)
	if err != nil {
		warning("Skipping %s: invalid Immich date %q: %v", file.Filename, asset.FileCreatedAt, err)
		return
	}

	photoTakenDate := tm.UTC().Format("2006-01-02")
	assetCreatedAtDate := assetCreatedAt.UTC().Format("2006-01-02")
	if photoTakenDate == assetCreatedAtDate {
		fmt.Printf("  %s %s\n", colorize(ansiGreen, "✓"), "No update needed for "+file.Filename)
		return
	}

	photoTaken := tm.UTC().Format(time.RFC3339Nano)
	info("Updating %s: %s → %s", file.Filename, asset.FileCreatedAt, photoTaken)
	if !YesNoPrompt("Are you sure you want to update this file?", false, true) {
		return
	}
	_, err = getJSONData("assets/"+asset.ID, http.MethodPut, struct {
		DateTimeOriginal string `json:"dateTimeOriginal"`
	}{DateTimeOriginal: photoTaken})
	if err != nil {
		failure("Could not update %s: %v", file.Filename, err)
		return
	}
	success("Updated %s", file.Filename)
}

func syncFilesDates() {
	section("📅", "Date synchronization")
	info("Checking Takeout timestamps against Immich...")
	for _, file := range takeoutFiles {
		if _, err := takeoutPhotoTime(file); err != nil {
			warning("Skipping %s: %v", file.Filename, err)
			continue
		}
		assets, err := findAssetByFilename(file.Filename, "", "")
		if err != nil {
			failure("%v", err)
			continue
		}
		if len(assets) == 0 {
			warning("Asset not found: %s", file.Filename)
			continue
		}
		if len(assets) > 1 {
			warning("More than one asset found: %s", file.Filename)
			continue
		}
		syncAssetDate(file, assets[0])
	}
}

func syncFilesByDate(date string) error {
	section("📅", "Date synchronization")
	info("Filtering Immich assets for %s", date)
	assets, err := findAssetsByDate(date)
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		warning("No assets found for date: %s", date)
		return nil
	}
	success("Found %d assets for %s", len(assets), date)
	suppressConfirmation = false
	filesByName := make(map[string][]TakeoutFile)
	for _, file := range takeoutFiles {
		filesByName[file.Filename] = append(filesByName[file.Filename], file)
	}
	for _, asset := range assets {
		files := filesByName[asset.OriginalFileName]
		switch len(files) {
		case 0:
			warning("No matching Takeout file for asset: %s", asset.OriginalFileName)
		case 1:
			syncAssetDate(files[0], asset)
		default:
			warning("More than one Takeout file matches asset: %s", asset.OriginalFileName)
		}
	}
	return nil
}

func syncAlbum(album Album, files []TakeoutFile, override bool) {
	if !override {
		section("🖼️", "Album: "+album.AlbumName)
	}
	if len(files) == 0 {
		warning("Album skipped: %s (no images found)", album.AlbumName)
		return
	}
	if !override && !confirmAlbumAction(fmt.Sprintf("Sync %d Takeout images into this album?", len(files))) {
		warning("Album skipped: %s", album.AlbumName)
		return
	}
	immichAssets, err := getAlbumAssets(album.ID)
	if err != nil {
		failure("%v", err)
		return
	}
	existingAssetIDs := make(map[string]struct{}, len(immichAssets))
	for _, asset := range immichAssets {
		existingAssetIDs[asset.ID] = struct{}{}
	}
	info("Already in album: %d assets", len(existingAssetIDs))

	newAssets := []string{}
	newAssetIDs := make(map[string]struct{})
	notFound := 0
	skipped := 0
	if assetSearchCache == nil {
		assetSearchCache = make(map[assetSearchQuery][]ImmichAsset)
	}
	queries := make([]assetSearchQuery, 0, len(files))
	uniqueQueries := make([]assetSearchQuery, 0, len(files))
	seenQueries := make(map[assetSearchQuery]struct{}, len(files))
	for _, file := range files {
		createdAfterTimestamp, err := parseTakeoutTimestamp(file.PhotoTakenTime, file.CreationTime)
		if err != nil {
			warning("Skipping %s: invalid photo timestamp: %v", file.Filename, err)
			continue
		}
		createdBeforeTimestamp, err := parseTakeoutTimestamp(file.CreationTime, file.PhotoTakenTime)
		if err != nil {
			warning("Skipping %s: invalid creation timestamp: %v", file.Filename, err)
			continue
		}
		tm1 := time.Unix(createdAfterTimestamp, 0)
		tm2 := time.Unix(createdBeforeTimestamp, 0)
		createdAfter := tm1.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		createdBefore := tm2.Add(24 * time.Hour).UTC().Format(time.RFC3339)
		query := assetSearchQuery{Filename: file.Filename, TakenAfter: createdAfter, TakenBefore: createdBefore}
		queries = append(queries, query)
		if _, ok := seenQueries[query]; !ok {
			seenQueries[query] = struct{}{}
			uniqueQueries = append(uniqueQueries, query)
		}
	}

	uncachedQueries := make([]assetSearchQuery, 0, len(uniqueQueries))
	for _, query := range uniqueQueries {
		if _, ok := assetSearchCache[query]; !ok {
			uncachedQueries = append(uncachedQueries, query)
		}
	}
	if len(uncachedQueries) > 0 {
		progressLabel := "Matching Immich assets"
		interactiveProgress := startProgress(progressLabel, len(uncachedQueries))
		fetchedAssets, err := searchAssetQueries(uncachedQueries, assetSearchWorkers, func(completed int) {
			updateProgress(interactiveProgress, progressLabel, completed, len(uncachedQueries))
		})
		finishProgress(interactiveProgress, progressLabel, len(uncachedQueries))
		for query, assets := range fetchedAssets {
			assetSearchCache[query] = assets
		}
		if err != nil {
			failure("%v", err)
			return
		}
	}
	if len(uniqueQueries) > len(uncachedQueries) {
		info("Reused %d cached asset searches", len(uniqueQueries)-len(uncachedQueries))
	}

	for _, query := range queries {
		assets := assetSearchCache[query]
		if len(assets) == 0 {
			notFound++
			continue
		}
		for _, asset := range assets {
			if _, ok := existingAssetIDs[asset.ID]; ok {
				skipped++
				continue
			}
			if _, ok := newAssetIDs[asset.ID]; ok {
				continue
			}
			newAssetIDs[asset.ID] = struct{}{}
			newAssets = append(newAssets, asset.ID)
		}
	}
	if skipped > 0 {
		info("Skipped %d assets already present", skipped)
	}
	if len(newAssets) > 0 {
		body, err := getJSONData("albums/"+album.ID+"/assets", http.MethodPut, struct {
			IDs []string `json:"ids"`
		}{IDs: newAssets})

		if err != nil {
			failure("%v", err)
			return
		}
		var response []struct {
			ID      string `json:"id"`
			Success bool   `json:"success"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			failure("Invalid album assets response: %v", err)
			return
		}
		i := 0
		for _, r := range response {
			if !r.Success {
				failure("Error syncing asset: %s", r.ID)
			} else {
				i++
			}
		}
		if override {
			success("Added %d assets to new album", i)
		} else {
			success("Album updated: %s (+%d assets)", album.AlbumName, i)
		}
	} else {
		if override {
			success("Album created: %s (no matching assets)", album.AlbumName)
		} else {
			success("Album unchanged: %s", album.AlbumName)
		}
	}
	if notFound > 0 {
		warning("%d assets not found in Immich", notFound)
	}
}

func createAlbum(albumName string, description string, files []TakeoutFile) {
	if len(files) == 0 {
		warning("Album skipped: %s (no images found; not created)", albumName)
		return
	}
	section("🖼️", "Album: "+albumName)
	info("New album: %s (%d Takeout images)", albumName, len(files))
	if !confirmAlbumAction(fmt.Sprintf("Create album and sync %d images?", len(files))) {
		warning("Album skipped: %s (not created)", albumName)
		return
	}
	body, err := getJSONData("albums", http.MethodPost, struct {
		AlbumName   string `json:"albumName"`
		Description string `json:"description,omitempty"`
	}{AlbumName: albumName, Description: description})
	if err != nil {
		failure("%v", err)
		return
	}
	var immichAlbum Album
	if err := json.Unmarshal(body, &immichAlbum); err != nil {
		failure("Invalid album response: %v", err)
		return
	}
	if strings.TrimSpace(immichAlbum.ID) == "" {
		failure("Invalid album response: missing album ID")
		return
	}
	success("Album created: %s", albumName)
	syncAlbum(immichAlbum, files, true)
}

func normalizeAPIURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("IMMICH_API_URL must be a valid HTTP(S) URL ending in /api, for example https://immich.example/api")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("IMMICH_API_URL must not contain a query string or fragment")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path != "/api" && !strings.HasSuffix(path, "/api") {
		return "", errors.New("IMMICH_API_URL must end with /api, for example https://immich.example/api")
	}
	parsed.Path = path + "/"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validateTakeoutPath(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", errors.New("IMMICH_TAKEOUT_PATH must point to the Google Photos directory")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("IMMICH_TAKEOUT_PATH %q cannot be read: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("IMMICH_TAKEOUT_PATH %q is not a directory", path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("IMMICH_TAKEOUT_PATH %q cannot be inspected: %w", path, err)
	}
	if googlePhotosInfo, googlePhotosErr := os.Stat(filepath.Join(path, "Google Photos")); googlePhotosErr == nil && googlePhotosInfo.IsDir() {
		return "", fmt.Errorf("IMMICH_TAKEOUT_PATH %q is the outer Takeout directory; use %q instead", path, filepath.Join(path, "Google Photos"))
	}
	albumDirectories := 0
	metadataDirectories := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		albumDirectories++
		metadataPath := filepath.Join(path, entry.Name(), "metadata.json")
		metadataInfo, metadataErr := os.Stat(metadataPath)
		if metadataErr == nil && !metadataInfo.IsDir() {
			metadataDirectories++
		}
	}
	if albumDirectories == 0 {
		return "", fmt.Errorf("IMMICH_TAKEOUT_PATH %q does not contain album directories; select the extracted Google Photos directory, not the outer Takeout folder", path)
	}
	if metadataDirectories == 0 {
		return "", fmt.Errorf("IMMICH_TAKEOUT_PATH %q does not look like a Google Photos Takeout directory; expected album subdirectories containing metadata.json", path)
	}
	return filepath.Clean(path), nil
}

func environmentValue(name string, prompt string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value != "" {
		return value, nil
	}
	value, ok := UserInput(prompt+" ("+name+")", "", true)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is not set and could not be read from the terminal", name)
	}
	return strings.TrimSpace(value), nil
}

func readEnvironment() error {
	var err error
	apiKey, err = environmentValue("IMMICH_API_KEY", "Enter Immich API key")
	if err != nil {
		return err
	}

	rawAPIURL, err := environmentValue("IMMICH_API_URL", "Enter Immich API URL")
	if err != nil {
		return err
	}
	apiURL, err = normalizeAPIURL(rawAPIURL)
	if err != nil {
		return err
	}

	rawTakeoutPath, err := environmentValue("IMMICH_TAKEOUT_PATH", "Enter Google Takeout path")
	if err != nil {
		return err
	}
	takeoutPath, err = validateTakeoutPath(rawTakeoutPath)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	section("📸", "Google Photos → Immich sync")
	if err := readEnvironment(); err != nil {
		failure("%v", err)
		return
	}
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.BoolVar(&suppressConfirmation, "y", false, "skip non-required confirmations")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return
	}

	body, err := getData("users/me", http.MethodGet, "")
	if err != nil {
		failure("%v", err)
		return
	}
	var response struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		failure("Invalid user response: %v", err)
		return
	}

	ok := YesNoPrompt("Are you "+response.Name+"?", false, true)
	if !ok {
		info("Bye-bye")
		return
	}

	ok = YesNoPrompt("Do you want to sync your albums?", true, true)
	if ok {
		if err := readFiles(false); err != nil {
			failure("%v", err)
			return
		}
		if len(takeout) == 0 {
			warning("No files found")
			return
		}
		albums, err := getAlbums()
		if err != nil {
			failure("%v", err)
			return
		}
		assetSearchCache = make(map[assetSearchQuery][]ImmichAsset)
		for _, albumName := range orderedAlbumNames(takeout, takeoutAlbumCreationTimes) {
			files := takeout[albumName]
			if len(files) == 0 {
				warning("Album skipped: %s (no images found)", albumName)
				continue
			}
			if album, found := findAlbumByName(albums, albumName); found {
				syncAlbum(album, files, false)
			} else {
				createAlbum(albumName, takeoutAlbumDescriptions[albumName], files)
			}
		}
		success("Album synchronization complete")
	} else {
		ok = YesNoPrompt("Do you want to sync assets dates?", true, true)
		if ok {
			if err := readFiles(true); err != nil {
				failure("%v", err)
				return
			}
			info("Found %d files", len(takeoutFiles))
			date, inputOK := UserInput("Enter date to filter files (YYYY-MM-DD) or leave empty to sync all files: ", "", true)
			if !inputOK {
				warning("Input ended; stopping")
				return
			}
			if date != "" {
				if err := syncFilesByDate(date); err != nil {
					failure("%v", err)
					return
				}
			} else {
				syncFilesDates()
			}
			success("Date synchronization complete")
		} else {
			info("Bye-bye")
			return
		}
	}

}
