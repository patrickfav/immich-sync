package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	suppressConfirmation = false
	approveAllAlbums     = false
	includeArchived      = true
	minimumMatchedImages = defaultMinimumMatchedImages
	runSummary           syncSummary
	stdinReader          = bufio.NewReader(os.Stdin)
)

const defaultMinimumMatchedImages = 3

type syncSummary struct {
	AlbumsCreated   int
	AlbumsUpdated   int
	AlbumsUnchanged int
	AlbumsSkipped   int
	AssetsAdded     int
	CommentsFound   int
	CommentsAdded   int
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

func minimumMatchedImagesPrompt() (int, bool) {
	for {
		value, ok := UserInput("Minimum matched images for new albums (0-100)", strconv.Itoa(defaultMinimumMatchedImages), true)
		if !ok {
			return 0, false
		}
		threshold, err := strconv.Atoi(value)
		if err == nil && threshold >= 0 && threshold <= 100 {
			return threshold, true
		}
		warning("Please enter a whole number between 0 and 100")
	}
}

func includeArchivedPrompt() bool {
	return YesNoPrompt("Include archived Google Photos items?", true, true)
}

func albumMeetsCreationThreshold(totalImages int, matchedImages int, threshold int) bool {
	if totalImages == 0 {
		return threshold == 0
	}
	return matchedImages == totalImages || matchedImages >= threshold
}

func printSyncSummary() {
	section("📊", "Sync summary")
	rows := []struct {
		icon  string
		label string
		value int
		color string
	}{
		{icon: "[+]", label: "Albums created", value: runSummary.AlbumsCreated, color: ansiGreen},
		{icon: "[~]", label: "Albums updated", value: runSummary.AlbumsUpdated, color: ansiCyan},
		{icon: "[=]", label: "Albums unchanged", value: runSummary.AlbumsUnchanged, color: ansiDim},
		{icon: "[!]", label: "Albums skipped", value: runSummary.AlbumsSkipped, color: ansiYellow},
		{icon: "[+]", label: "New assets added", value: runSummary.AssetsAdded, color: ansiGreen},
		{icon: "[?]", label: "Comments found", value: runSummary.CommentsFound, color: ansiCyan},
		{icon: "[+]", label: "Comments added", value: runSummary.CommentsAdded, color: ansiGreen},
	}
	for _, row := range rows {
		value := colorize(row.color, fmt.Sprintf("%6d", row.value))
		fmt.Printf("  %-3s  %-20s │ %s\n", colorize(row.color, row.icon), row.label, value)
	}
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

type matchedTakeoutAsset struct {
	File  TakeoutFile
	Asset ImmichAsset
}

func findAlbumAssetMatches(files []TakeoutFile) ([]matchedTakeoutAsset, int, error) {
	if assetSearchCache == nil {
		assetSearchCache = make(map[assetSearchQuery][]ImmichAsset)
	}
	queries := make([]assetSearchQuery, 0, len(files))
	queryFiles := make([]TakeoutFile, 0, len(files))
	uniqueQueries := make([]assetSearchQuery, 0, len(files))
	seenQueries := make(map[assetSearchQuery]struct{}, len(files))
	notFound := 0
	for _, file := range files {
		createdAfterTimestamp, err := parseTakeoutTimestamp(file.PhotoTakenTime, file.CreationTime)
		if err != nil {
			warning("Skipping %s: invalid photo timestamp: %v", file.Filename, err)
			notFound++
			continue
		}
		createdBeforeTimestamp, err := parseTakeoutTimestamp(file.CreationTime, file.PhotoTakenTime)
		if err != nil {
			warning("Skipping %s: invalid creation timestamp: %v", file.Filename, err)
			notFound++
			continue
		}
		tm1 := time.Unix(createdAfterTimestamp, 0)
		tm2 := time.Unix(createdBeforeTimestamp, 0)
		createdAfter := tm1.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		createdBefore := tm2.Add(24 * time.Hour).UTC().Format(time.RFC3339)
		query := assetSearchQuery{Filename: file.Filename, TakenAfter: createdAfter, TakenBefore: createdBefore}
		queries = append(queries, query)
		queryFiles = append(queryFiles, file)
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
			return nil, 0, err
		}
	}
	if len(uniqueQueries) > len(uncachedQueries) {
		info("Reused %d cached asset searches", len(uniqueQueries)-len(uncachedQueries))
	}

	matches := make([]matchedTakeoutAsset, 0)
	for fileIndex, query := range queries {
		assets := assetSearchCache[query]
		matched := false
		for _, asset := range assets {
			if asset.ID == "" {
				continue
			}
			matched = true
			matches = append(matches, matchedTakeoutAsset{File: queryFiles[fileIndex], Asset: asset})
		}
		if !matched {
			notFound++
		}
	}
	return matches, notFound, nil
}

func findAlbumAssetIDs(files []TakeoutFile) ([]string, int, error) {
	matches, notFound, err := findAlbumAssetMatches(files)
	if err != nil {
		return nil, 0, err
	}
	assetIDs := make([]string, 0, len(matches))
	seen := make(map[string]struct{})
	for _, match := range matches {
		if _, ok := seen[match.Asset.ID]; ok {
			continue
		}
		seen[match.Asset.ID] = struct{}{}
		assetIDs = append(assetIDs, match.Asset.ID)
	}
	return assetIDs, notFound, nil
}

func formatMigratedComment(comment TakeoutComment) string {
	text := strings.TrimSpace(comment.Text)
	author := strings.TrimSpace(comment.Author)
	if author == "" {
		return text + " [migrated from Google Photos]"
	}
	return author + ": " + text + " [migrated from Google Photos]"
}

func migrateComments(target activityTarget, comments []TakeoutComment) {
	if len(comments) == 0 {
		return
	}
	if activityCommentCache == nil {
		activityCommentCache = make(map[activityTarget]map[string]struct{})
	}
	if activityCommentFoundCache == nil {
		activityCommentFoundCache = make(map[activityTarget]map[string]struct{})
	}
	found := activityCommentFoundCache[target]
	if found == nil {
		found = make(map[string]struct{})
		activityCommentFoundCache[target] = found
	}
	for _, sourceComment := range comments {
		comment := formatMigratedComment(sourceComment)
		if strings.TrimSpace(comment) == "[migrated from Google Photos]" {
			continue
		}
		if _, ok := found[comment]; !ok {
			found[comment] = struct{}{}
			runSummary.CommentsFound++
		}
	}
	existing, cached := activityCommentCache[target]
	if !cached {
		activities, err := getActivities(target)
		if err != nil {
			warning("Could not read existing Immich comments: %v", err)
			return
		}
		existing = make(map[string]struct{}, len(activities))
		for _, activity := range activities {
			if strings.EqualFold(activity.Type, "comment") && strings.TrimSpace(activity.Comment) != "" {
				existing[activity.Comment] = struct{}{}
			}
		}
		activityCommentCache[target] = existing
	}
	for _, sourceComment := range comments {
		comment := formatMigratedComment(sourceComment)
		if strings.TrimSpace(comment) == "[migrated from Google Photos]" {
			continue
		}
		if _, ok := existing[comment]; ok {
			continue
		}
		if err := createActivityComment(target, comment); err != nil {
			warning("Could not add migrated comment: %v", err)
			continue
		}
		existing[comment] = struct{}{}
		runSummary.CommentsAdded++
	}
}

func migrateAssetComments(albumID string, matches []matchedTakeoutAsset) {
	for _, match := range matches {
		migrateComments(activityTarget{AssetID: match.Asset.ID, AlbumID: albumID}, match.File.Comments)
	}
}

func migrateAlbumComments(album Album) {
	migrateComments(activityTarget{AlbumID: album.ID}, takeoutAlbumComments[album.AlbumName])
}

func addAssetsToAlbum(album Album, assetIDs []string, override bool, notFound int) {
	if len(assetIDs) == 0 {
		if override {
			warning("Album skipped: %s (no matching Immich images; not created)", album.AlbumName)
		} else {
			runSummary.AlbumsUnchanged++
			success("Album unchanged: %s", album.AlbumName)
		}
		if notFound > 0 {
			warning("%d assets not found in Immich", notFound)
		}
		return
	}

	body, err := getJSONData("albums/"+album.ID+"/assets", http.MethodPut, struct {
		IDs []string `json:"ids"`
	}{IDs: assetIDs})

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
	runSummary.AssetsAdded += i
	if override {
		success("Added %d assets to new album", i)
	} else if i > 0 {
		runSummary.AlbumsUpdated++
		success("Album updated: %s (+%d assets)", album.AlbumName, i)
	} else {
		runSummary.AlbumsUnchanged++
		success("Album unchanged: %s", album.AlbumName)
	}
	if notFound > 0 {
		warning("%d assets not found in Immich", notFound)
	}
}

func syncAlbum(album Album, files []TakeoutFile, override bool) {
	if !override {
		section("🖼️", "Album: "+album.AlbumName)
	}
	if len(files) == 0 {
		runSummary.AlbumsSkipped++
		warning("Album skipped: %s (no images found)", album.AlbumName)
		migrateAlbumComments(album)
		return
	}
	if !override && !confirmAlbumAction(fmt.Sprintf("Sync %d Takeout images into this album?", len(files))) {
		runSummary.AlbumsSkipped++
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

	matchedAssets, notFound, err := findAlbumAssetMatches(files)
	if err != nil {
		return
	}
	matchedAssetIDs := make([]string, 0, len(matchedAssets))
	matchedAssetIDsSet := make(map[string]struct{})
	for _, match := range matchedAssets {
		if _, ok := matchedAssetIDsSet[match.Asset.ID]; ok {
			continue
		}
		matchedAssetIDsSet[match.Asset.ID] = struct{}{}
		matchedAssetIDs = append(matchedAssetIDs, match.Asset.ID)
	}
	newAssets := make([]string, 0, len(matchedAssetIDs))
	skipped := 0
	for _, assetID := range matchedAssetIDs {
		if _, ok := existingAssetIDs[assetID]; ok {
			skipped++
			continue
		}
		newAssets = append(newAssets, assetID)
	}
	if skipped > 0 {
		info("Skipped %d assets already present", skipped)
	}
	addAssetsToAlbum(album, newAssets, override, notFound)
	migrateAssetComments(album.ID, matchedAssets)
	migrateAlbumComments(album)
}

func createAlbum(albumName string, description string, files []TakeoutFile) {
	if len(files) == 0 && minimumMatchedImages > 0 {
		runSummary.AlbumsSkipped++
		warning("Album skipped: %s (no images found; not created)", albumName)
		return
	}
	section("🖼️", "Album: "+albumName)
	if len(files) == 0 {
		info("New album: %s (empty Takeout album)", albumName)
	} else {
		info("New album: %s (%d Takeout images)", albumName, len(files))
	}
	confirmationLabel := fmt.Sprintf("Create album and sync %d images?", len(files))
	if len(files) == 0 {
		confirmationLabel = "Create empty album?"
	}
	if !confirmAlbumAction(confirmationLabel) {
		runSummary.AlbumsSkipped++
		warning("Album skipped: %s (not created)", albumName)
		return
	}
	var matchedAssetIDs []string
	var matchedAssets []matchedTakeoutAsset
	notFound := 0
	if len(files) > 0 {
		var err error
		matchedAssets, notFound, err = findAlbumAssetMatches(files)
		if err != nil {
			return
		}
		matchedAssetIDs = make([]string, 0, len(matchedAssets))
		seenAssetIDs := make(map[string]struct{})
		for _, match := range matchedAssets {
			if _, ok := seenAssetIDs[match.Asset.ID]; ok {
				continue
			}
			seenAssetIDs[match.Asset.ID] = struct{}{}
			matchedAssetIDs = append(matchedAssetIDs, match.Asset.ID)
		}
	}
	matchedImages := len(files) - notFound
	if !albumMeetsCreationThreshold(len(files), matchedImages, minimumMatchedImages) {
		runSummary.AlbumsSkipped++
		warning("Album skipped: %s (%d/%d images matched; minimum is %d)", albumName, matchedImages, len(files), minimumMatchedImages)
		if notFound > 0 {
			warning("%d assets not found in Immich", notFound)
		}
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
	runSummary.AlbumsCreated++
	migrateAssetComments(immichAlbum.ID, matchedAssets)
	migrateAlbumComments(immichAlbum)
	if len(matchedAssetIDs) == 0 {
		info("Album created with no matched assets: %s", albumName)
		if notFound > 0 {
			warning("%d assets not found in Immich", notFound)
		}
		return
	}
	addAssetsToAlbum(immichAlbum, matchedAssetIDs, true, notFound)
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
	source := localTakeoutSource{}
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.BoolVar(&suppressConfirmation, "y", false, "skip non-required confirmations")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return
	}
	includeArchived = includeArchivedPrompt()

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
		threshold, inputOK := minimumMatchedImagesPrompt()
		if !inputOK {
			warning("Input ended; stopping")
			return
		}
		minimumMatchedImages = threshold
		info("New albums require %d matched images unless all Takeout images match", minimumMatchedImages)
		data, err := source.ReadAlbums()
		if err != nil {
			failure("%v", err)
			return
		}
		if len(data.Albums) == 0 {
			warning("No files found")
			return
		}
		takeoutAlbumComments = data.Comments
		albums, err := getAlbums()
		if err != nil {
			failure("%v", err)
			return
		}
		assetSearchCache = make(map[assetSearchQuery][]ImmichAsset)
		activityCommentCache = make(map[activityTarget]map[string]struct{})
		activityCommentFoundCache = make(map[activityTarget]map[string]struct{})
		runSummary = syncSummary{}
		for _, albumName := range orderedAlbumNames(data.Albums, data.CreationTimes) {
			files := data.Albums[albumName]
			if len(files) == 0 && minimumMatchedImages > 0 {
				runSummary.AlbumsSkipped++
				warning("Album skipped: %s (no images found)", albumName)
				continue
			}
			if album, found := findAlbumByName(albums, albumName); found {
				syncAlbum(album, files, false)
			} else {
				createAlbum(albumName, data.Descriptions[albumName], files)
			}
		}
		success("Album synchronization complete")
		printSyncSummary()
	} else {
		ok = YesNoPrompt("Do you want to sync assets dates?", true, true)
		if ok {
			files, err := source.ReadFiles()
			if err != nil {
				failure("%v", err)
				return
			}
			takeoutFiles = files
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
