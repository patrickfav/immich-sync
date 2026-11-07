package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writeTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func TestTakeoutFilenameFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		want     string
		wantOkay bool
	}{
		{
			name:     "SNY00032_24MP.jpg.supplemental-metadata.json",
			want:     "SNY00032_24MP.jpg",
			wantOkay: true,
		},
		{
			name:     "SNY00032_24MP.jpg.suppl.json",
			want:     "SNY00032_24MP.jpg",
			wantOkay: true,
		},
		{
			name:     "SNY00032_24MP.jpg.suppl(1).json",
			want:     "SNY00032_24MP.jpg",
			wantOkay: true,
		},
		{
			name:     "metadata.json",
			wantOkay: false,
		},
		{
			name:     "SNY00032_24MP.jpg",
			wantOkay: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := takeoutFilenameFromMetadata(tt.name)
			if got != tt.want || ok != tt.wantOkay {
				t.Fatalf("takeoutFilenameFromMetadata(%q) = %q, %t; want %q, %t", tt.name, got, ok, tt.want, tt.wantOkay)
			}
		})
	}
}

func TestProgressBar(t *testing.T) {
	if got := progressBar(0, 10); got != "────────────────────────" {
		t.Fatalf("progressBar(0, 10) = %q, want empty bar", got)
	}
	if got := progressBar(5, 10); got != "━━━━━━━━━━━━────────────" {
		t.Fatalf("progressBar(5, 10) = %q, want half-filled bar", got)
	}
	if got := progressBar(10, 10); got != "━━━━━━━━━━━━━━━━━━━━━━━━" {
		t.Fatalf("progressBar(10, 10) = %q, want full bar", got)
	}
}

func TestReadFilesDiscoversAlbumSidecars(t *testing.T) {
	oldTakeout := takeout
	oldTakeoutAlbumCreationTimes := takeoutAlbumCreationTimes
	oldTakeoutAlbumDescriptions := takeoutAlbumDescriptions
	oldTakeoutPath := takeoutPath
	t.Cleanup(func() {
		takeout = oldTakeout
		takeoutAlbumCreationTimes = oldTakeoutAlbumCreationTimes
		takeoutAlbumDescriptions = oldTakeoutAlbumDescriptions
		takeoutPath = oldTakeoutPath
	})

	root := t.TempDir()
	albumPath := filepath.Join(root, "Auf Kreta mit Theo #2025")
	writeTestFile(t, filepath.Join(albumPath, "metadata.json"), `{"title":"Auf Kreta mit Theo #2025","description":"A day in Crete","date":{"timestamp":"1760044090"}}`)
	writeTestFile(t, filepath.Join(albumPath, "SNY00032_24MP.jpg.supplemental-metadata.json"), `{
  "title": "SNY00032_24MP.jpg",
  "creationTime": {"timestamp": "1760044090"},
  "photoTakenTime": {"timestamp": "1759504776"}
}`)
	writeTestFile(t, filepath.Join(albumPath, "not-a-photo.json"), `{"title":"should-not-be-imported"}`)

	takeout = nil
	takeoutPath = root
	if err := readFiles(false); err != nil {
		t.Fatalf("readFiles() failed: %v", err)
	}

	files := takeout["Auf Kreta mit Theo #2025"]
	if len(files) != 1 {
		t.Fatalf("discovered %d album files, want 1", len(files))
	}
	if files[0].Filename != "SNY00032_24MP.jpg" {
		t.Fatalf("discovered filename %q, want SNY00032_24MP.jpg", files[0].Filename)
	}
	if files[0].PhotoTakenTime != "1759504776" || files[0].CreationTime != "1760044090" {
		t.Fatalf("discovered timestamps = %q/%q, want 1759504776/1760044090", files[0].PhotoTakenTime, files[0].CreationTime)
	}
	if got := takeoutAlbumDescriptions["Auf Kreta mit Theo #2025"]; got != "A day in Crete" {
		t.Fatalf("discovered album description = %q, want %q", got, "A day in Crete")
	}
	if got := takeoutAlbumCreationTimes["Auf Kreta mit Theo #2025"]; got != 1760044090 {
		t.Fatalf("discovered album timestamp = %d, want %d", got, 1760044090)
	}
}

func TestReadAllDirSupportsGoogleSidecarVariants(t *testing.T) {
	oldTakeoutFiles := takeoutFiles
	t.Cleanup(func() { takeoutFiles = oldTakeoutFiles })

	root := t.TempDir()
	metadata := `{
  "title": "photo.jpg",
  "creationTime": {"timestamp": "1760044090"},
  "photoTakenTime": {"timestamp": "1759504776"}
}`
	writeTestFile(t, filepath.Join(root, "one.jpg.supplemental-metadata.json"), metadata)
	writeTestFile(t, filepath.Join(root, "nested", "two.jpg.suppl.json"), metadata)
	writeTestFile(t, filepath.Join(root, "nested", "three.jpg.suppl(1).json"), metadata)

	takeoutFiles = nil
	if err := readAllDir(root); err != nil {
		t.Fatalf("readAllDir() failed: %v", err)
	}

	got := make(map[string]bool, len(takeoutFiles))
	for _, file := range takeoutFiles {
		got[file.Filename] = true
	}
	for _, want := range []string{"one.jpg", "two.jpg", "three.jpg"} {
		if !got[want] {
			t.Errorf("missing discovered filename %q", want)
		}
	}
	if len(takeoutFiles) != 3 {
		t.Fatalf("discovered %d files, want 3", len(takeoutFiles))
	}
}

func TestFindAlbumByName(t *testing.T) {
	albums := []Album{
		{ID: "first", AlbumName: "Holiday"},
		{ID: "second", AlbumName: "Work"},
	}

	got, ok := findAlbumByName(albums, "Work")
	if !ok || got.ID != "second" {
		t.Fatalf("findAlbumByName() = %#v, %t; want Work album, true", got, ok)
	}
	if _, ok := findAlbumByName(albums, "Missing"); ok {
		t.Fatal("findAlbumByName() found a missing album")
	}
}

func TestOrderedAlbumNamesUsesNewestCreationTime(t *testing.T) {
	albums := map[string][]TakeoutFile{
		"Older":  nil,
		"Newer":  nil,
		"Undated": nil,
	}
	creationTimes := map[string]int64{
		"Older": 100,
		"Newer": 200,
	}

	want := []string{"Newer", "Older", "Undated"}
	got := orderedAlbumNames(albums, creationTimes)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("orderedAlbumNames() = %v, want %v", got, want)
	}
}

func TestOrderedAlbumNamesFallsBackToLexicographicOrder(t *testing.T) {
	albums := map[string][]TakeoutFile{
		"Zulu":  nil,
		"Alpha": nil,
		"Mango": nil,
	}

	want := []string{"Alpha", "Mango", "Zulu"}
	got := orderedAlbumNames(albums, nil)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("orderedAlbumNames() = %v, want %v", got, want)
	}
}

func TestConfirmAlbumActionApprovesRemainingAlbums(t *testing.T) {
	oldApproveAllAlbums := approveAllAlbums
	oldSuppressConfirmation := suppressConfirmation
	oldStdinReader := stdinReader
	t.Cleanup(func() {
		approveAllAlbums = oldApproveAllAlbums
		suppressConfirmation = oldSuppressConfirmation
		stdinReader = oldStdinReader
	})

	approveAllAlbums = false
	suppressConfirmation = false
	stdinReader = bufio.NewReader(strings.NewReader("A\n"))

	if !confirmAlbumAction("sync album") {
		t.Fatal("confirmAlbumAction() rejected A")
	}
	if !approveAllAlbums {
		t.Fatal("confirmAlbumAction() did not enable approval for remaining albums")
	}
	if !confirmAlbumAction("create album") {
		t.Fatal("confirmAlbumAction() prompted result was not reused")
	}
}

func TestSyncAlbumAddsOnlyMissingAssets(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldAssetSearchCache := assetSearchCache
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		assetSearchCache = oldAssetSearchCache
	})

	var addedIDs []string
	var filenameSearches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/metadata" && r.URL.Path != "/albums/album-id/assets" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}

		if r.Method == http.MethodPut {
			var payload struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode add-assets request: %v", err)
			}
			addedIDs = payload.IDs
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"new-id","success":true}]`))
			return
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode search request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, ok := payload["albumIds"]; ok {
			_, _ = w.Write([]byte(`{"assets":{"total":1,"count":1,"items":[{"id":"existing-id","originalFileName":"already.jpg"}]}}`))
			return
		}
		filename := payload["originalFileName"]
		if filename == "already.jpg" {
			_, _ = w.Write([]byte(`{"assets":{"total":1,"count":1,"items":[{"id":"existing-id","originalFileName":"already.jpg"}]}}`))
			filenameSearches.Add(1)
			return
		}
		filenameSearches.Add(1)
		_, _ = w.Write([]byte(`{"assets":{"total":1,"count":1,"items":[{"id":"new-id","originalFileName":"new.jpg"}]}}`))
	}))
	defer server.Close()

	apiURL = server.URL + "/"
	apiKey = "test-key"
	httpClient = server.Client()

	syncAlbum(Album{ID: "album-id", AlbumName: "Holiday"}, []TakeoutFile{
		{Filename: "already.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
		{Filename: "new.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
		{Filename: "new.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
	}, true)

	if len(addedIDs) != 1 || addedIDs[0] != "new-id" {
		t.Fatalf("added asset IDs = %v, want [new-id]", addedIDs)
	}
	if got := filenameSearches.Load(); got != 2 {
		t.Fatalf("filename search requests = %d, want 2 for two unique queries", got)
	}
}

func TestCreateAlbumSkipsEmptyAlbum(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldApproveAllAlbums := approveAllAlbums
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		approveAllAlbums = oldApproveAllAlbums
	})

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	apiURL = server.URL + "/"
	apiKey = "test-key"
	httpClient = server.Client()
	approveAllAlbums = true

	createAlbum("Empty album", "should not be sent", nil)
	if called {
		t.Fatal("createAlbum() made an API request for an empty album")
	}
}

func TestCreateAlbumSendsDescription(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldApproveAllAlbums := approveAllAlbums
	oldAssetSearchCache := assetSearchCache
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		approveAllAlbums = oldApproveAllAlbums
		assetSearchCache = oldAssetSearchCache
	})

	var description string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/albums":
			var payload struct {
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create album request: %v", err)
			}
			description = payload.Description
			_, _ = w.Write([]byte(`{"id":"album-id","albumName":"Holiday"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/search/metadata":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode search request: %v", err)
			}
			if _, ok := payload["albumIds"]; ok {
				_, _ = w.Write([]byte(`{"assets":{"total":0,"count":0,"items":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"assets":{"total":1,"count":1,"items":[{"id":"asset-id","originalFileName":"photo.jpg"}]}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/albums/album-id/assets":
			_, _ = w.Write([]byte(`[{"id":"asset-id","success":true}]`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	apiURL = server.URL + "/"
	apiKey = "test-key"
	httpClient = server.Client()
	approveAllAlbums = true

	createAlbum("Holiday", "A day in Crete", []TakeoutFile{{
		Filename:       "photo.jpg",
		CreationTime:   "1760044090",
		PhotoTakenTime: "1759504776",
	}})

	if description != "A day in Crete" {
		t.Fatalf("created album description = %q, want %q", description, "A day in Crete")
	}
}

func TestAPIEndpointNormalizesSlashes(t *testing.T) {
	oldAPIURL := apiURL
	t.Cleanup(func() { apiURL = oldAPIURL })

	apiURL = "https://immich.example/api///"
	got, err := apiEndpoint("/search/metadata")
	if err != nil {
		t.Fatalf("apiEndpoint() returned an error: %v", err)
	}
	if got != "https://immich.example/api/search/metadata" {
		t.Fatalf("apiEndpoint() = %q, want normalized endpoint", got)
	}
}

func TestFindAssetsByDatePaginates(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
	})

	var pages []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/metadata" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Page int `json:"page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode search request: %v", err)
		}
		pages = append(pages, payload.Page)
		w.Header().Set("Content-Type", "application/json")
		if payload.Page == 1 {
			_, _ = w.Write([]byte(`{"assets":{"total":2,"count":1,"items":[{"id":"first"}],"nextPage":"2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"assets":{"total":2,"count":1,"items":[{"id":"second"}]}}`))
	}))
	defer server.Close()

	apiURL = server.URL
	apiKey = "test-key"
	httpClient = server.Client()

	assets, err := findAssetsByDate("2025-10-03")
	if err != nil {
		t.Fatalf("findAssetsByDate() returned an error: %v", err)
	}
	if len(assets) != 2 || assets[0].ID != "first" || assets[1].ID != "second" {
		t.Fatalf("findAssetsByDate() = %#v, want both pages", assets)
	}
	if len(pages) != 2 || strings.Join([]string{fmt.Sprint(pages[0]), fmt.Sprint(pages[1])}, ",") != "1,2" {
		t.Fatalf("requested pages = %v, want [1 2]", pages)
	}
}

func TestParseTakeoutTimestampUsesFallback(t *testing.T) {
	got, err := parseTakeoutTimestamp("", "1760044090")
	if err != nil {
		t.Fatalf("parseTakeoutTimestamp() returned an error: %v", err)
	}
	if got != 1760044090 {
		t.Fatalf("parseTakeoutTimestamp() = %d, want 1760044090", got)
	}
	if _, err := parseTakeoutTimestamp("bad", ""); err == nil {
		t.Fatal("parseTakeoutTimestamp() accepted invalid timestamps")
	}
}
