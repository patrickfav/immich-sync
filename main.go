package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Asset struct {
	Title        *string `json:"title"`
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
	Total int           `json:"total"`
	Count int           `json:"count"`
	Items []ImmichAsset `json:"items"`
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
	OwnerID   string `json:"ownerId"`
	Assets    []struct {
		ID               string `json:"id"`
		OriginalFileName string `json:"originalFileName"`
		OriginalMimeType string `json:"originalMimeType"`
	} `json:"assets"`
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

func getData(path string, method string, payload string) ([]byte, error) {
	url := apiURL + path
	req, err := http.NewRequest(method, url, strings.NewReader(payload))
	if apiKey == "" {
		fmt.Println("No API key set")
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
		fmt.Fprintf(os.Stderr, "%s (%s) ", label, choices)
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
		fmt.Fprintf(os.Stderr, "%s (Y/n/A) ", label)
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

func UserInput(label string, def string, required bool) string {
	var s string

	for {
		if def != "" {
			fmt.Fprintf(os.Stderr, "%s (default: %s): ", label, def)
		} else {
			fmt.Fprintf(os.Stderr, "%s: ", label)
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
	fmt.Println("Getting Immich albums...")
	body, err := getData("albums", "GET", "")
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	if err := json.Unmarshal(body, &albums); err != nil {
		return nil, fmt.Errorf("invalid albums response: %w", err)
	}
	fmt.Printf("Total albums: %d\n", len(albums))
	return albums, nil
}

func readFiles(readAll bool) {
	basePath := takeoutPath
	entries, err := os.ReadDir(basePath)
	if err != nil {
		log.Fatal(err)
	}
	if takeout == nil {
		takeout = make(map[string][]TakeoutFile)
	}
	fmt.Println("Reading takeout albums...")
	albumsTotal := 0
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
				fmt.Printf("Could not read album metadata %s: %v\n", entryPath, err)
			}
			continue
		}
		if asset.Title != nil && *asset.Title != "" {
			albumsTotal++
			readAlbum(entryPath, *asset.Title)
		}
	}
	if albumsTotal == 0 {
		fmt.Println("No albums found")
	} else {
		fmt.Printf("Found %d albums\n", albumsTotal)
	}

}

func readAlbum(albumPath string, albumTitle string) {

	fmt.Println("Found album: " + albumTitle)
	entries, err := os.ReadDir(albumPath)
	if err != nil {
		log.Fatal(err)
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
			fmt.Printf("Could not read photo metadata %s: %v\n", entry.Name(), err)
			continue
		}
		if asset.Title != nil && *asset.Title != "" {
			takeout[albumTitle] = append(takeout[albumTitle], TakeoutFile{Filename: *asset.Title, CreationTime: asset.CreationTime.Timestamp, PhotoTakenTime: asset.PhotoTakenTime.Timestamp})
		}
	}
}

func readAllDir(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		log.Fatal(err)
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
				fmt.Printf("Could not read photo metadata %s: %v\n", entryPath, err)
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
		fmt.Println(err)
		return
	}
	var response ImmichAlbumAsset
	if err := json.Unmarshal(body, &response); err != nil {
		fmt.Println("Invalid Immich search response:", err)
		return
	}
	if response.Assets.Total == 0 {
		// fmt.Println("Asset not found: " + filename)
		return
	}
	return response.Assets.Items
}

func findAssetsByDate(date string) (assets []ImmichAsset) {
	payload := struct {
		TakenAfter  string `json:"takenAfter"`
		TakenBefore string `json:"takenBefore"`
	}{TakenAfter: date + "T00:00:00Z", TakenBefore: date + "T23:59:59Z"}
	body, err := getJSONData("search/metadata", "POST", payload)
	if err != nil {
		fmt.Println(err)
		return []ImmichAsset{}
	}
	var response ImmichAlbumAsset
	if err := json.Unmarshal(body, &response); err != nil {
		fmt.Println("Invalid Immich search response:", err)
		return []ImmichAsset{}
	}
	if response.Assets.Total == 0 {
		return []ImmichAsset{}
	}
	return response.Assets.Items
}

func syncFilesDates() {
	fmt.Println("Syncing files dates...")
	for _, file := range takeoutFiles {
		photoTakenTimestamp, err1 := strconv.ParseInt(file.PhotoTakenTime, 10, 64)
		if err1 != nil {
			fmt.Println("Error parsing timestamp:", err1)
			return
		}
		assets := findAssetByFilename(file.Filename, "", "")
		if len(assets) == 0 {
			fmt.Println("Asset not found: " + file.Filename)
			continue
		}
		if len(assets) > 1 {
			fmt.Println("More than one asset found: " + file.Filename)
			continue
		}
		asset := assets[0]
		tm := time.Unix(photoTakenTimestamp, 0)
		photoTakenDate := tm.UTC().Format("2006-01-02")
		assetCreatedAtDateObj, err := time.Parse("2006-01-02T15:04:05.000Z", asset.FileCreatedAt)
		if err != nil {
			fmt.Println("Error parsing date:", err)
			continue
		}
		assetCreatedAtDate := assetCreatedAtDateObj.UTC().Format("2006-01-02")
		if photoTakenDate != assetCreatedAtDate {
			photoTaken := tm.UTC().Format("2006-01-02T15:04:05.000Z")
			fmt.Printf("Updating %s: %s -> %s\n", file.Filename, asset.FileCreatedAt, photoTaken)
			ok := YesNoPrompt("Are you sure you want to update this file?", false, true)
			if !ok {
				continue
			}
			_, err := getJSONData("assets/"+asset.ID, "PUT", struct {
				DateTimeOriginal string `json:"dateTimeOriginal"`
			}{DateTimeOriginal: photoTaken})
			if err != nil {
				fmt.Println(err)
			}
		} else {
			fmt.Printf("No update needed for %s\n", file.Filename)
		}
	}
}

func syncFilesByDate(date string) {
	fmt.Println("Syncing files by date: " + date)
	assets := findAssetsByDate(date)
	if len(assets) == 0 {
		fmt.Println("No assets found for date: " + date)
		return
	}
	fmt.Printf("Found %d assets for date: %s\n", len(assets), date)
	suppressConfirmation = false
	for _, asset := range assets {
		found := false
		for _, file := range takeoutFiles {
			if asset.OriginalFileName == file.Filename {
				found = true
				photoTakenTimestamp, err1 := strconv.ParseInt(file.PhotoTakenTime, 10, 64)
				if err1 != nil {
					fmt.Println("Error parsing timestamp:", err1)
					return
				}
				tm := time.Unix(photoTakenTimestamp, 0)
				photoTakenDate := tm.UTC().Format("2006-01-02")
				assetCreatedAtDateObj, err := time.Parse("2006-01-02T15:04:05.000Z", asset.FileCreatedAt)
				if err != nil {
					fmt.Println("Error parsing date:", err)
					continue
				}
				assetCreatedAtDate := assetCreatedAtDateObj.UTC().Format("2006-01-02")
				if photoTakenDate != assetCreatedAtDate {
					photoTaken := tm.UTC().Format("2006-01-02T15:04:05.000Z")
					fmt.Printf("Updating %s: %s -> %s\n", file.Filename, asset.FileCreatedAt, photoTaken)
					ok := YesNoPrompt("Are you sure you want to update this file?", false, true)
					if !ok {
						continue
					}
					_, err := getJSONData("assets/"+asset.ID, "PUT", struct {
						DateTimeOriginal string `json:"dateTimeOriginal"`
					}{DateTimeOriginal: photoTaken})
					if err != nil {
						fmt.Println(err)
					}
				} else {
					fmt.Printf("No update needed for %s\n", file.Filename)
				}
				break
			}
		}
		if !found {
			fmt.Println("No matching file found in takeout for asset: " + asset.OriginalFileName)
		}
	}
}

func syncAlbum(album Album, files []TakeoutFile, override bool) {
	fmt.Println("Syncing album: " + album.AlbumName)
	if !override && !approveAllAlbums {
		ok, approveAll := AlbumConfirmation("Are you sure you want to sync this album?")
		if approveAll {
			approveAllAlbums = true
		}
		if !ok {
			return
		}
	}
	body, err := getData("albums/"+album.ID, "GET", "")
	if err != nil {
		fmt.Println(err)
		return
	}
	newAssets := []string{}
	var immichAlbum Album
	if err := json.Unmarshal(body, &immichAlbum); err != nil {
		fmt.Println("Invalid album response:", err)
		return
	}
	notFound := 0
	fmt.Print("Searching files ")
	for _, file := range files {
		found := false
		for _, asset := range immichAlbum.Assets {
			if asset.OriginalFileName == file.Filename {
				found = true
				break
			}
		}
		if !found {
			fmt.Print(".")
			createdAfterTimestamp, err1 := strconv.ParseInt(file.PhotoTakenTime, 10, 64)
			createdBeforeTimestamp, err2 := strconv.ParseInt(file.CreationTime, 10, 64)
			if err1 != nil || err2 != nil {
				fmt.Println("Error parsing timestamp:", err1, err2)
				return
			}
			tm1 := time.Unix(createdAfterTimestamp, 0)
			tm2 := time.Unix(createdBeforeTimestamp, 0)
			createdAfter := tm1.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
			createdBefore := tm2.Add(24 * time.Hour).UTC().Format(time.RFC3339)

			assets := findAssetByFilename(file.Filename, createdAfter, createdBefore)
			if len(assets) == 0 {
				notFound++
			}
			var ids []string
			for _, asset := range assets {
				ids = append(ids, asset.ID)

			}

			newAssets = append(newAssets, ids...)
			continue
		}
	}
	fmt.Println(" Done")
	if len(newAssets) > 0 {
		body, err := getJSONData("albums/"+album.ID+"/assets", "PUT", struct {
			IDs []string `json:"ids"`
		}{IDs: newAssets})

		if err != nil {
			fmt.Println(err)
			return
		}
		var response []struct {
			ID      string `json:"id"`
			Success bool   `json:"success"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			fmt.Println("Invalid album assets response:", err)
			return
		}
		i := 0
		for _, r := range response {
			if !r.Success {
				fmt.Println("Error syncing asset: " + r.ID)
			} else {
				i++
			}
		}
		fmt.Println("Synced " + strconv.Itoa(i) + " assets to " + album.AlbumName)
	} else {
		fmt.Println("No new assets added to " + album.AlbumName)
	}
	if notFound > 0 {
		fmt.Println("Warning: " + strconv.Itoa(notFound) + " assets not found")
	}
}

func createAlbum(albumName string, files []TakeoutFile) {
	fmt.Println("Creating album: " + albumName)
	ok := YesNoPrompt("Are you sure you want to create this album?", true, false)
	if !ok {
		return
	}
	body, err := getJSONData("albums", "POST", struct {
		AlbumName string `json:"albumName"`
	}{AlbumName: albumName})
	if err != nil {
		fmt.Println(err)
		return
	}
	var immichAlbum Album
	if err := json.Unmarshal(body, &immichAlbum); err != nil {
		fmt.Println("Invalid album response:", err)
		return
	}
	fmt.Printf("New album created: %s\n", immichAlbum.ID)
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
			fmt.Println("Invalid config.json:", err)
			os.Exit(1)
		}
		apiKey = config.APIKey
		apiURL = config.APIURL
		takeoutPath = config.TakeoutPath
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		fmt.Println("Could not read config.json:", err)
		os.Exit(1)
	}

	fmt.Println("No config file found, trying ENV vars...")
	apiKey = os.Getenv("IMMICH_API_KEY")
	apiURL = os.Getenv("IMMICH_API_URL")
	takeoutPath = os.Getenv("IMMICH_TAKEOUT_PATH")
	if apiKey == "" {
		fmt.Println("No IMMICH_API_KEY set")
		os.Exit(1)
	}
	if apiURL == "" {
		fmt.Println("No IMMICH_API_URL set")
		os.Exit(1)
	}
	if takeoutPath == "" {
		fmt.Println("No IMMICH_TAKEOUT_PATH set, using current directory")
		takeoutPath = "."
	}
}

func main() {
	readConfig()

	if len(os.Args) > 1 && os.Args[1] == "-y" {
		suppressConfirmation = true
	}

	body, err := getData("users/me", "GET", "")
	if err != nil {
		fmt.Println(err)
		return
	}
	var response struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		fmt.Println("Invalid user response:", err)
		return
	}

	ok := YesNoPrompt("Are you "+response.Name+"?", false, true)
	if !ok {
		fmt.Println("Bye-bye...")
		return
	}

	ok = YesNoPrompt("Do you want to sync your albums?", true, true)
	if ok {
		readFiles(false)
		if len(takeout) == 0 {
			fmt.Println("No files found")
			return
		}
		albums, err := getAlbums()
		if err != nil {
			fmt.Println(err)
			return
		}
		for albumName, files := range takeout {
			foundAlbum := false
			for _, album := range albums {
				if album.AlbumName == albumName {
					syncAlbum(album, files, false)
					foundAlbum = true
					break
				}
			}
			if !foundAlbum {
				createAlbum(albumName, files)
			}
		}
		fmt.Println("Done!")
	} else {
		ok = YesNoPrompt("Do you want to sync assets dates?", true, true)
		if ok {
			readFiles(true)
			fmt.Println("Found total files: " + strconv.Itoa(len(takeoutFiles)))
			date := UserInput("Enter date to filter files (YYYY-MM-DD) or leave empty to sync all files: ", "", true)
			if date != "" {
				syncFilesByDate(date)
			} else {
				syncFilesDates()
			}
			fmt.Println("Done!")
		} else {
			fmt.Println("Bye-bye...")
			return
		}
	}

}
