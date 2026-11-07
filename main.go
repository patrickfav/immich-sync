package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

var httpClient = &http.Client{}

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

func fatal(format string, args ...any) {
	failure(format, args...)
	os.Exit(1)
}

func promptPrefix() string {
	return colorize(ansiBlue, "?")
}

func getData(path string, method string, payload string) ([]byte, error) {
	url := apiURL + path
	req, err := http.NewRequest(method, url, strings.NewReader(payload))
	if apiKey == "" {
		failure("No API key set")
		os.Exit(1)
	}

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
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {

		return nil, err
	}
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Immich API %s %s returned HTTP %s: %s", method, path, res.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
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
		s, _ = stdinReader.ReadString('\n')
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
		s, _ := stdinReader.ReadString('\n')
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

func UserInput(label string, def string, required bool) string {
	var s string

	for {
		if def != "" {
			fmt.Fprintf(os.Stderr, "%s %s (default: %s): ", promptPrefix(), label, def)
		} else {
			fmt.Fprintf(os.Stderr, "%s %s: ", promptPrefix(), label)
		}
		s, _ = stdinReader.ReadString('\n')
		s = strings.TrimSpace(s)
		if s == "" {
			if def != "" {
				return def
			}
			if required {
				continue
			} else {
				return ""
			}
		}
		return s
	}
}

func getAlbums() (albums []Album, err error) {
	section("🗂️", "Immich albums")
	info("Loading albums...")
	body, err := getData("albums", "GET", "")
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

func readFiles(readAll bool) {
	basePath := takeoutPath
	entries, err := os.ReadDir(basePath)
	if err != nil {
		fatal("Could not read Takeout directory: %v", err)
	}
	if takeout == nil {
		takeout = make(map[string][]TakeoutFile)
	}
	if takeoutAlbumCreationTimes == nil {
		takeoutAlbumCreationTimes = make(map[string]int64)
	}
	if takeoutAlbumDescriptions == nil {
		takeoutAlbumDescriptions = make(map[string]string)
	}
	section("📦", "Google Takeout")
	info("Reading album metadata...")
	albumsTotal := 0
	imagesTotal := 0
	for _, entry := range entries {
		entryPath := filepath.Join(basePath, entry.Name())
		if readAll && entry.IsDir() {
			readAllDir(entryPath)
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
			readAlbum(entryPath, *asset.Title)
			imagesTotal += len(takeout[*asset.Title])
		}
	}
	if albumsTotal == 0 {
		warning("No albums found")
	} else {
		success("Found %d albums with %d images", albumsTotal, imagesTotal)
	}

}

func readAlbum(albumPath string, albumTitle string) {
	entries, err := os.ReadDir(albumPath)
	if err != nil {
		fatal("Could not read album directory: %v", err)
	}
	if _, ok := takeout[albumTitle]; !ok {
		takeout[albumTitle] = []TakeoutFile{}
	}
	for _, entry := range entries {
		if entry.Name() == "metadata.json" || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var asset Asset
		if err := readJSONFile(filepath.Join(albumPath, entry.Name()), &asset); err != nil {
			warning("Could not read photo metadata %s: %v", entry.Name(), err)
			continue
		}
		if asset.Title != nil && *asset.Title != "" {
			takeout[albumTitle] = append(takeout[albumTitle], TakeoutFile{Filename: *asset.Title, CreationTime: asset.CreationTime.Timestamp, PhotoTakenTime: asset.PhotoTakenTime.Timestamp})
		}
	}
	imageCount := len(takeout[albumTitle])
	fmt.Printf("  %s %-*s %s\n", colorize(ansiBlue, "▸"), 48, albumTitle, colorize(ansiDim, fmt.Sprintf("%d images", imageCount)))
}

func readAllDir(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		fatal("Could not read Takeout directory: %v", err)
	}
	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			readAllDir(entryPath)
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
			if asset.Title != nil && *asset.Title != "" && asset.PhotoTakenTime.Timestamp != "" {
				takeoutFiles = append(takeoutFiles, TakeoutFile{Filename: fileName, CreationTime: asset.CreationTime.Timestamp, PhotoTakenTime: asset.PhotoTakenTime.Timestamp})
			}
		}
	}
}

func findAssetByFilename(filename string, createdAfter string, createdBefore string) (asset []ImmichAsset) {
	payload := struct {
		OriginalFileName string `json:"originalFileName"`
		TakenAfter       string `json:"takenAfter,omitempty"`
		TakenBefore      string `json:"takenBefore,omitempty"`
	}{OriginalFileName: filename, TakenAfter: createdAfter, TakenBefore: createdBefore}
	body, err := getJSONData("search/metadata", "POST", payload)
	if err != nil {
		failure("%v", err)
		return
	}
	var response ImmichAlbumAsset
	if err := json.Unmarshal(body, &response); err != nil {
		failure("Invalid Immich search response: %v", err)
		return
	}
	if response.Assets.Total == 0 {
		// fmt.Println("Asset not found: " + filename)
		return
	}
	return response.Assets.Items
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

		body, err := getJSONData("search/metadata", "POST", payload)
		if err != nil {
			return nil, err
		}

		var response ImmichAlbumAsset
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("invalid Immich album assets response: %w", err)
		}
		assets = append(assets, response.Assets.Items...)

		if response.Assets.NextPage == nil || *response.Assets.NextPage == "" {
			return assets, nil
		}
		nextPage, err := strconv.Atoi(*response.Assets.NextPage)
		if err != nil {
			return nil, fmt.Errorf("invalid nextPage in Immich album assets response: %w", err)
		}
		if nextPage <= page {
			return nil, fmt.Errorf("invalid nextPage %d in Immich album assets response", nextPage)
		}
		page = nextPage
	}
}

func findAssetsByDate(date string) (assets []ImmichAsset) {
	payload := struct {
		TakenAfter  string `json:"takenAfter"`
		TakenBefore string `json:"takenBefore"`
	}{TakenAfter: date + "T00:00:00Z", TakenBefore: date + "T23:59:59Z"}
	body, err := getJSONData("search/metadata", "POST", payload)
	if err != nil {
		failure("%v", err)
		return []ImmichAsset{}
	}
	var response ImmichAlbumAsset
	if err := json.Unmarshal(body, &response); err != nil {
		failure("Invalid Immich search response: %v", err)
		return []ImmichAsset{}
	}
	if response.Assets.Total == 0 {
		return []ImmichAsset{}
	}
	return response.Assets.Items
}

func syncFilesDates() {
	section("📅", "Date synchronization")
	info("Checking Takeout timestamps against Immich...")
	for _, file := range takeoutFiles {
		photoTakenTimestamp, err1 := strconv.ParseInt(file.PhotoTakenTime, 10, 64)
		if err1 != nil {
			failure("Error parsing timestamp: %v", err1)
			return
		}
		assets := findAssetByFilename(file.Filename, "", "")
		if len(assets) == 0 {
			warning("Asset not found: %s", file.Filename)
			continue
		}
		if len(assets) > 1 {
			warning("More than one asset found: %s", file.Filename)
			continue
		}
		asset := assets[0]
		tm := time.Unix(photoTakenTimestamp, 0)
		photoTakenDate := tm.UTC().Format("2006-01-02")
		assetCreatedAtDateObj, err := time.Parse("2006-01-02T15:04:05.000Z", asset.FileCreatedAt)
		if err != nil {
			failure("Error parsing Immich date: %v", err)
			continue
		}
		assetCreatedAtDate := assetCreatedAtDateObj.UTC().Format("2006-01-02")
		if photoTakenDate != assetCreatedAtDate {
			photoTaken := tm.UTC().Format("2006-01-02T15:04:05.000Z")
			info("Updating %s: %s → %s", file.Filename, asset.FileCreatedAt, photoTaken)
			ok := YesNoPrompt("Are you sure you want to update this file?", false, true)
			if !ok {
				continue
			}
			_, err := getJSONData("assets/"+asset.ID, "PUT", struct {
				DateTimeOriginal string `json:"dateTimeOriginal"`
			}{DateTimeOriginal: photoTaken})
			if err != nil {
				failure("%v", err)
			} else {
				success("Updated %s", file.Filename)
			}
		} else {
			fmt.Printf("  %s %s\n", colorize(ansiGreen, "✓"), "No update needed for "+file.Filename)
		}
	}
}

func syncFilesByDate(date string) {
	section("📅", "Date synchronization")
	info("Filtering Immich assets for %s", date)
	assets := findAssetsByDate(date)
	if len(assets) == 0 {
		warning("No assets found for date: %s", date)
		return
	}
	success("Found %d assets for %s", len(assets), date)
	suppressConfirmation = false
	for _, asset := range assets {
		found := false
		for _, file := range takeoutFiles {
			if asset.OriginalFileName == file.Filename {
				found = true
				photoTakenTimestamp, err1 := strconv.ParseInt(file.PhotoTakenTime, 10, 64)
				if err1 != nil {
					failure("Error parsing timestamp: %v", err1)
					return
				}
				tm := time.Unix(photoTakenTimestamp, 0)
				photoTakenDate := tm.UTC().Format("2006-01-02")
				assetCreatedAtDateObj, err := time.Parse("2006-01-02T15:04:05.000Z", asset.FileCreatedAt)
				if err != nil {
					failure("Error parsing Immich date: %v", err)
					continue
				}
				assetCreatedAtDate := assetCreatedAtDateObj.UTC().Format("2006-01-02")
				if photoTakenDate != assetCreatedAtDate {
					photoTaken := tm.UTC().Format("2006-01-02T15:04:05.000Z")
					info("Updating %s: %s → %s", file.Filename, asset.FileCreatedAt, photoTaken)
					ok := YesNoPrompt("Are you sure you want to update this file?", false, true)
					if !ok {
						continue
					}
					_, err := getJSONData("assets/"+asset.ID, "PUT", struct {
						DateTimeOriginal string `json:"dateTimeOriginal"`
					}{DateTimeOriginal: photoTaken})
					if err != nil {
						failure("%v", err)
					} else {
						success("Updated %s", file.Filename)
					}
				} else {
					fmt.Printf("  %s %s\n", colorize(ansiGreen, "✓"), "No update needed for "+file.Filename)
				}
				break
			}
		}
		if !found {
			warning("No matching Takeout file for asset: %s", asset.OriginalFileName)
		}
	}
}

func syncAlbum(album Album, files []TakeoutFile, override bool) {
	section("🖼️", "Album: "+album.AlbumName)
	if len(files) == 0 {
		warning("No images found in Takeout album; skipping")
		return
	}
	if !override && !confirmAlbumAction(fmt.Sprintf("Sync %d Takeout images into this album?", len(files))) {
		warning("Skipped album: %s", album.AlbumName)
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
	progressLabel := "Matching Takeout images"
	interactiveProgress := startProgress(progressLabel, len(files))
	for index, file := range files {
		createdAfterTimestamp, err1 := strconv.ParseInt(file.PhotoTakenTime, 10, 64)
		createdBeforeTimestamp, err2 := strconv.ParseInt(file.CreationTime, 10, 64)
		if err1 != nil || err2 != nil {
			stopProgress(interactiveProgress)
			failure("Error parsing timestamps: %v / %v", err1, err2)
			return
		}
		tm1 := time.Unix(createdAfterTimestamp, 0)
		tm2 := time.Unix(createdBeforeTimestamp, 0)
		createdAfter := tm1.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		createdBefore := tm2.Add(24 * time.Hour).UTC().Format(time.RFC3339)

		assets := findAssetByFilename(file.Filename, createdAfter, createdBefore)
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
		updateProgress(interactiveProgress, progressLabel, index+1, len(files))
	}
	finishProgress(interactiveProgress, progressLabel, len(files))
	if skipped > 0 {
		info("Skipped %d assets already present", skipped)
	}
	if len(newAssets) > 0 {
		body, err := getJSONData("albums/"+album.ID+"/assets", "PUT", struct {
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
		success("Added %d assets", i)
	} else {
		info("No new assets to add")
	}
	if notFound > 0 {
		warning("%d assets not found in Immich", notFound)
	}
}

func createAlbum(albumName string, description string, files []TakeoutFile) {
	if len(files) == 0 {
		warning("No images found in Takeout album; not creating it")
		return
	}
	info("New album: %s (%d Takeout images)", albumName, len(files))
	if !confirmAlbumAction(fmt.Sprintf("Create album and sync %d images?", len(files))) {
		warning("Skipped album: %s", albumName)
		return
	}
	body, err := getJSONData("albums", "POST", struct {
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
	success("Created album")
	syncAlbum(immichAlbum, files, true)
}

func readConfig() {
	bytefile, err := os.ReadFile("config.json")
	if err == nil {
		var config struct {
			APIKey      string `json:"apiKey"`
			APIURL      string `json:"apiURL"`
			TakeoutPath string `json:"takeoutPath"`
		}
		if err := json.Unmarshal(bytefile, &config); err != nil {
			failure("Invalid config.json: %v", err)
			os.Exit(1)
		}
		apiKey = config.APIKey
		apiURL = config.APIURL
		takeoutPath = config.TakeoutPath
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		failure("Could not read config.json: %v", err)
		os.Exit(1)
	}

	info("No config file found; using environment variables")
	apiKey = os.Getenv("IMMICH_API_KEY")
	apiURL = os.Getenv("IMMICH_API_URL")
	takeoutPath = os.Getenv("IMMICH_TAKEOUT_PATH")
	if apiKey == "" {
		failure("No IMMICH_API_KEY set")
		os.Exit(1)
	}
	if apiURL == "" {
		failure("No IMMICH_API_URL set")
		os.Exit(1)
	}
	if takeoutPath == "" {
		info("No IMMICH_TAKEOUT_PATH set; using current directory")
		takeoutPath = "."
	}
}

func main() {
	section("📸", "Google Photos → Immich sync")
	readConfig()

	if len(os.Args) > 1 && os.Args[1] == "-y" {
		suppressConfirmation = true
	}

	body, err := getData("users/me", "GET", "")
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
		readFiles(false)
		if len(takeout) == 0 {
			warning("No files found")
			return
		}
		albums, err := getAlbums()
		if err != nil {
			failure("%v", err)
			return
		}
		for _, albumName := range orderedAlbumNames(takeout, takeoutAlbumCreationTimes) {
			files := takeout[albumName]
			if len(files) == 0 {
				warning("No images found in Takeout album %q; skipping", albumName)
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
			readFiles(true)
			info("Found %d files", len(takeoutFiles))
			date := UserInput("Enter date to filter files (YYYY-MM-DD) or leave empty to sync all files: ", "", true)
			if date != "" {
				syncFilesByDate(date)
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
