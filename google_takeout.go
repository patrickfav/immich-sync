package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Asset struct {
	Title               *string              `json:"title"`
	Description         string               `json:"description"`
	Archived            bool                 `json:"archived"`
	SharedAlbumComments []SharedAlbumComment `json:"sharedAlbumComments"`
	Date                struct {
		Timestamp string `json:"timestamp"`
	} `json:"date"`
	CreationTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"creationTime"`
	PhotoTakenTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
}

type SharedAlbumComment struct {
	Text             string `json:"text"`
	ContentOwnerName string `json:"contentOwnerName"`
}

type TakeoutComment struct {
	Text   string
	Author string
}

type TakeoutFile struct {
	Filename       string
	CreationTime   string
	PhotoTakenTime string
	Archived       bool
	Comments       []TakeoutComment
}

var takeout map[string][]TakeoutFile

var takeoutAlbumCreationTimes map[string]int64

var takeoutAlbumDescriptions map[string]string

var takeoutAlbumComments map[string][]TakeoutComment

var takeoutFiles []TakeoutFile

var takeoutPath string

func readJSONFile(path string, value any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, value)
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
		for _, marker := range []string{".supplemental-metadata(", ".suppl("} {
			if suffixStart := strings.LastIndex(base, marker); suffixStart >= 0 && strings.HasSuffix(base, ")") {
				return base[:suffixStart], true
			}
		}
	}

	return "", false
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
	takeoutAlbumComments = make(map[string][]TakeoutComment)
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
			takeoutAlbumComments[*asset.Title] = takeoutComments(asset.SharedAlbumComments)
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
		if !includeArchived && asset.Archived {
			continue
		}
		if fileName != "" {
			takeout[albumTitle] = append(takeout[albumTitle], TakeoutFile{Filename: fileName, CreationTime: asset.CreationTime.Timestamp, PhotoTakenTime: asset.PhotoTakenTime.Timestamp, Archived: asset.Archived, Comments: takeoutComments(asset.SharedAlbumComments)})
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
			if !includeArchived && asset.Archived {
				continue
			}
			if asset.PhotoTakenTime.Timestamp != "" {
				takeoutFiles = append(takeoutFiles, TakeoutFile{Filename: fileName, CreationTime: asset.CreationTime.Timestamp, PhotoTakenTime: asset.PhotoTakenTime.Timestamp, Archived: asset.Archived, Comments: takeoutComments(asset.SharedAlbumComments)})
			}
		}
	}
	return nil
}

func takeoutComments(comments []SharedAlbumComment) []TakeoutComment {
	result := make([]TakeoutComment, 0, len(comments))
	for _, comment := range comments {
		text := strings.TrimSpace(comment.Text)
		if text == "" {
			continue
		}
		result = append(result, TakeoutComment{Text: text, Author: strings.TrimSpace(comment.ContentOwnerName)})
	}
	return result
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

type sourceData struct {
	Albums        map[string][]TakeoutFile
	CreationTimes map[string]int64
	Descriptions  map[string]string
	Comments      map[string][]TakeoutComment
}

type localTakeoutSource struct{}

func (localTakeoutSource) ReadAlbums() (sourceData, error) {
	if err := readFiles(false); err != nil {
		return sourceData{}, err
	}
	return sourceData{
		Albums:        takeout,
		CreationTimes: takeoutAlbumCreationTimes,
		Descriptions:  takeoutAlbumDescriptions,
		Comments:      takeoutAlbumComments,
	}, nil
}

func (localTakeoutSource) ReadFiles() ([]TakeoutFile, error) {
	if err := readFiles(true); err != nil {
		return nil, err
	}
	return append([]TakeoutFile(nil), takeoutFiles...), nil
}
