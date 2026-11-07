package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

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
	minRateLimitJitter           = 750 * time.Millisecond
	maxRateLimitJitter           = 3 * time.Second
)

var (
	apiURL           string
	apiKey           string
	httpClient       = &http.Client{Timeout: 30 * time.Second}
	assetSearchCache map[assetSearchQuery][]ImmichAsset
)

type rateLimitState struct {
	sync.Mutex
	until time.Time
}

var rateLimitCooldown rateLimitState

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

func waitForRateLimitCooldown() {
	waitForRateLimitCooldownState(&rateLimitCooldown)
}

func waitForRateLimitCooldownState(state *rateLimitState) {
	for {
		state.Lock()
		wait := time.Until(state.until)
		state.Unlock()
		if wait <= 0 {
			return
		}
		time.Sleep(wait)
	}
}

func setRateLimitCooldown(delay time.Duration) {
	setRateLimitCooldownState(&rateLimitCooldown, delay)
}

func setRateLimitCooldownState(state *rateLimitState, delay time.Duration) {
	if delay <= 0 {
		return
	}

	state.Lock()
	defer state.Unlock()
	until := time.Now().Add(delay)
	if until.After(state.until) {
		state.until = until
	}
}

func addRateLimitJitter(delay time.Duration) time.Duration {
	jitterRange := int64(maxRateLimitJitter - minRateLimitJitter + time.Nanosecond)
	return delay + minRateLimitJitter + time.Duration(rand.Int63n(jitterRange))
}

func rateLimitDelay(headers http.Header, attempt int, now time.Time) (time.Duration, string) {
	if retryAfter := strings.TrimSpace(headers.Get("Retry-After")); retryAfter != "" {
		if seconds, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && seconds >= 0 {
			return addRateLimitJitter(time.Duration(seconds) * time.Second), "Retry-After"
		}
		if retryAt, err := http.ParseTime(retryAfter); err == nil {
			delay := retryAt.Sub(now)
			if delay < 0 {
				delay = 0
			}
			return addRateLimitJitter(delay), "Retry-After"
		}
	}

	if reset := strings.TrimSpace(headers.Get("X-RateLimit-Reset")); reset != "" {
		if seconds, err := strconv.ParseInt(reset, 10, 64); err == nil {
			if delay := time.Unix(seconds, 0).Sub(now); delay > 0 {
				return addRateLimitJitter(delay), "X-RateLimit-Reset"
			}
			return addRateLimitJitter(0), "X-RateLimit-Reset"
		}
	}

	delay := defaultRateLimitCooldown
	for range attempt {
		if delay >= maxFallbackRateLimitCooldown/2 {
			return addRateLimitJitter(maxFallbackRateLimitCooldown), "fallback"
		}
		delay *= 2
	}
	return addRateLimitJitter(delay), "fallback"
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
