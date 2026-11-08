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
	"time"
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
			name:     "SNY00032_24MP.jpg.supplemental-metadata(1).json",
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

func TestMinimumMatchedImagesPromptValidatesRange(t *testing.T) {
	oldStdinReader := stdinReader
	t.Cleanup(func() { stdinReader = oldStdinReader })

	stdinReader = bufio.NewReader(strings.NewReader("101\n0\n"))
	got, ok := minimumMatchedImagesPrompt()
	if !ok || got != 0 {
		t.Fatalf("minimumMatchedImagesPrompt() = %d, %t; want 0, true", got, ok)
	}
}

func TestIncludeArchivedPromptDefaultsToInclude(t *testing.T) {
	oldStdinReader := stdinReader
	t.Cleanup(func() { stdinReader = oldStdinReader })

	stdinReader = bufio.NewReader(strings.NewReader("\n"))
	if !includeArchivedPrompt() {
		t.Fatal("includeArchivedPrompt() rejected the default include choice")
	}
}

func TestIncludeArchivedPromptCanExclude(t *testing.T) {
	oldStdinReader := stdinReader
	t.Cleanup(func() { stdinReader = oldStdinReader })

	stdinReader = bufio.NewReader(strings.NewReader("n\n"))
	if includeArchivedPrompt() {
		t.Fatal("includeArchivedPrompt() accepted archived items after n")
	}
}

func TestAlbumMeetsCreationThreshold(t *testing.T) {
	tests := []struct {
		name    string
		total   int
		matched int
		limit   int
		want    bool
	}{
		{name: "all images match", total: 5, matched: 5, limit: 100, want: true},
		{name: "partial match reaches limit", total: 5, matched: 3, limit: 3, want: true},
		{name: "partial match below limit", total: 5, matched: 2, limit: 3, want: false},
		{name: "zero allows unmatched images", total: 5, matched: 0, limit: 0, want: true},
		{name: "zero allows empty album", total: 0, matched: 0, limit: 0, want: true},
		{name: "nonzero rejects empty album", total: 0, matched: 0, limit: 3, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := albumMeetsCreationThreshold(tt.total, tt.matched, tt.limit); got != tt.want {
				t.Fatalf("albumMeetsCreationThreshold(%d, %d, %d) = %t, want %t", tt.total, tt.matched, tt.limit, got, tt.want)
			}
		})
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

func TestReadFilesFiltersArchivedAlbumSidecars(t *testing.T) {
	oldTakeout := takeout
	oldTakeoutAlbumCreationTimes := takeoutAlbumCreationTimes
	oldTakeoutAlbumDescriptions := takeoutAlbumDescriptions
	oldTakeoutPath := takeoutPath
	oldIncludeArchived := includeArchived
	t.Cleanup(func() {
		takeout = oldTakeout
		takeoutAlbumCreationTimes = oldTakeoutAlbumCreationTimes
		takeoutAlbumDescriptions = oldTakeoutAlbumDescriptions
		takeoutPath = oldTakeoutPath
		includeArchived = oldIncludeArchived
	})

	root := t.TempDir()
	albumPath := filepath.Join(root, "Holiday")
	writeTestFile(t, filepath.Join(albumPath, "metadata.json"), `{"title":"Holiday"}`)
	writeTestFile(t, filepath.Join(albumPath, "active.jpg.supplemental-metadata.json"), `{"title":"active.jpg","photoTakenTime":{"timestamp":"1759504776"}}`)
	writeTestFile(t, filepath.Join(albumPath, "archived.jpg.supplemental-metadata.json"), `{"title":"archived.jpg","archived":true,"photoTakenTime":{"timestamp":"1759504776"}}`)
	takeoutPath = root

	includeArchived = false
	if err := readFiles(false); err != nil {
		t.Fatalf("readFiles() excluding archived items failed: %v", err)
	}
	files := takeout["Holiday"]
	if len(files) != 1 || files[0].Filename != "active.jpg" {
		t.Fatalf("filtered album files = %#v, want only active.jpg", files)
	}

	includeArchived = true
	if err := readFiles(false); err != nil {
		t.Fatalf("readFiles() including archived items failed: %v", err)
	}
	if files = takeout["Holiday"]; len(files) != 2 {
		t.Fatalf("unfiltered album files = %d, want 2", len(files))
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
		"Older":   nil,
		"Newer":   nil,
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
	oldApproveAllAlbums := approveAllAlbums
	oldRunSummary := runSummary
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		assetSearchCache = oldAssetSearchCache
		approveAllAlbums = oldApproveAllAlbums
		runSummary = oldRunSummary
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
	approveAllAlbums = true

	syncAlbum(Album{ID: "album-id", AlbumName: "Holiday"}, []TakeoutFile{
		{Filename: "already.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
		{Filename: "new.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
		{Filename: "new.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
	}, false)

	if len(addedIDs) != 1 || addedIDs[0] != "new-id" {
		t.Fatalf("added asset IDs = %v, want [new-id]", addedIDs)
	}
	if got := filenameSearches.Load(); got != 2 {
		t.Fatalf("filename search requests = %d, want 2 for two unique queries", got)
	}
	if runSummary.AlbumsUpdated != 1 || runSummary.AssetsAdded != 1 {
		t.Fatalf("run summary = %#v, want one updated album and one added asset", runSummary)
	}
}

func TestCreateAlbumSkipsEmptyAlbum(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldApproveAllAlbums := approveAllAlbums
	oldRunSummary := runSummary
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		approveAllAlbums = oldApproveAllAlbums
		runSummary = oldRunSummary
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
	runSummary = syncSummary{}

	createAlbum("Empty album", "should not be sent", nil)
	if called {
		t.Fatal("createAlbum() made an API request for an empty album")
	}
}

func TestCreateAlbumSkipsWhenNoImmichAssetsMatch(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldApproveAllAlbums := approveAllAlbums
	oldAssetSearchCache := assetSearchCache
	oldRunSummary := runSummary
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		approveAllAlbums = oldApproveAllAlbums
		assetSearchCache = oldAssetSearchCache
		runSummary = oldRunSummary
	})

	var albumCreated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/search/metadata":
			_, _ = w.Write([]byte(`{"assets":{"total":0,"count":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/albums":
			albumCreated = true
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	apiURL = server.URL
	apiKey = "test-key"
	httpClient = server.Client()
	approveAllAlbums = true
	assetSearchCache = nil
	runSummary = syncSummary{}

	createAlbum("Unmatched album", "should not be created", []TakeoutFile{{
		Filename:       "missing.jpg",
		CreationTime:   "1760044090",
		PhotoTakenTime: "1759504776",
	}})

	if albumCreated {
		t.Fatal("createAlbum() created an album without matching Immich assets")
	}
}

func TestCreateAlbumSkipsPartialMatchBelowThreshold(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldApproveAllAlbums := approveAllAlbums
	oldAssetSearchCache := assetSearchCache
	oldMinimumMatchedImages := minimumMatchedImages
	oldRunSummary := runSummary
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		approveAllAlbums = oldApproveAllAlbums
		assetSearchCache = oldAssetSearchCache
		minimumMatchedImages = oldMinimumMatchedImages
		runSummary = oldRunSummary
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/albums" {
			t.Fatal("createAlbum() created an album below the matched-image threshold")
		}
		if r.URL.Path != "/search/metadata" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		var payload struct {
			OriginalFileName string `json:"originalFileName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode search request: %v", err)
		}
		if payload.OriginalFileName == "match-1.jpg" || payload.OriginalFileName == "match-2.jpg" {
			_, _ = w.Write([]byte(`{"assets":{"total":1,"count":1,"items":[{"id":"` + payload.OriginalFileName + `","originalFileName":"` + payload.OriginalFileName + `"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"assets":{"total":0,"count":0,"items":[]}}`))
	}))
	defer server.Close()

	apiURL = server.URL
	apiKey = "test-key"
	httpClient = server.Client()
	approveAllAlbums = true
	assetSearchCache = nil
	minimumMatchedImages = 3
	runSummary = syncSummary{}

	files := make([]TakeoutFile, 0, 5)
	for _, filename := range []string{"match-1.jpg", "match-2.jpg", "missing-1.jpg", "missing-2.jpg", "missing-3.jpg"} {
		files = append(files, TakeoutFile{Filename: filename, CreationTime: "1760044090", PhotoTakenTime: "1759504776"})
	}
	createAlbum("Partial album", "", files)

}

func TestCreateAlbumAllowsEmptyAlbumWithZeroThreshold(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldApproveAllAlbums := approveAllAlbums
	oldMinimumMatchedImages := minimumMatchedImages
	oldRunSummary := runSummary
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		approveAllAlbums = oldApproveAllAlbums
		minimumMatchedImages = oldMinimumMatchedImages
		runSummary = oldRunSummary
	})

	var albumCreated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/albums" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		albumCreated = true
		_, _ = w.Write([]byte(`{"id":"album-id","albumName":"Empty album"}`))
	}))
	defer server.Close()

	apiURL = server.URL
	apiKey = "test-key"
	httpClient = server.Client()
	approveAllAlbums = true
	minimumMatchedImages = 0
	runSummary = syncSummary{}

	createAlbum("Empty album", "", nil)

	if !albumCreated {
		t.Fatal("createAlbum() did not create an empty album with a zero threshold")
	}
}

func TestCreateAlbumSendsDescription(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldApproveAllAlbums := approveAllAlbums
	oldAssetSearchCache := assetSearchCache
	oldRunSummary := runSummary
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		approveAllAlbums = oldApproveAllAlbums
		assetSearchCache = oldAssetSearchCache
		runSummary = oldRunSummary
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
	assetSearchCache = nil
	runSummary = syncSummary{}

	createAlbum("Holiday", "A day in Crete", []TakeoutFile{{
		Filename:       "photo.jpg",
		CreationTime:   "1760044090",
		PhotoTakenTime: "1759504776",
	}})

	if description != "A day in Crete" {
		t.Fatalf("created album description = %q, want %q", description, "A day in Crete")
	}
	if runSummary.AlbumsCreated != 1 || runSummary.AssetsAdded != 1 {
		t.Fatalf("run summary = %#v, want one created album and one added asset", runSummary)
	}
}

func TestMigrateCommentsUsesCurrentUserAndIsIdempotent(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	oldActivityCommentCache := activityCommentCache
	oldActivityCommentFoundCache := activityCommentFoundCache
	oldRunSummary := runSummary
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		activityCommentCache = oldActivityCommentCache
		activityCommentFoundCache = oldActivityCommentFoundCache
		runSummary = oldRunSummary
	})

	var created []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/activities":
			_, _ = w.Write([]byte(`[{"type":"comment","comment":"Jamie Example: Already there [migrated from Google Photos]"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/activities":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create activity request: %v", err)
			}
			created = append(created, payload)
			_, _ = w.Write([]byte(`{"id":"activity-id"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	apiURL = server.URL
	apiKey = "test-key"
	httpClient = server.Client()
	activityCommentCache = nil
	activityCommentFoundCache = nil
	runSummary = syncSummary{}

	migrateComments(activityTarget{AssetID: "asset-id", AlbumID: "album-id"}, []TakeoutComment{
		{Author: "Jamie Example", Text: "Already there"},
		{Author: "Alex Example", Text: "A useful comment"},
	})

	if len(created) != 1 {
		t.Fatalf("created activities = %d, want 1", len(created))
	}
	if created[0]["assetId"] != "asset-id" || created[0]["albumId"] != "album-id" || created[0]["type"] != "comment" {
		t.Fatalf("created activity target/type = %#v", created[0])
	}
	if created[0]["comment"] != "Alex Example: A useful comment [migrated from Google Photos]" {
		t.Fatalf("created comment = %q", created[0]["comment"])
	}
	if runSummary.CommentsFound != 2 || runSummary.CommentsAdded != 1 {
		t.Fatalf("comments found/added = %d/%d, want 2/1", runSummary.CommentsFound, runSummary.CommentsAdded)
	}

	migrateComments(activityTarget{AssetID: "asset-id", AlbumID: "album-id"}, []TakeoutComment{
		{Author: "Jamie Example", Text: "Already there"},
		{Author: "Alex Example", Text: "A useful comment"},
	})
	if len(created) != 1 || runSummary.CommentsAdded != 1 {
		t.Fatalf("rerun created activities/comments added = %d/%d, want 1/1", len(created), runSummary.CommentsAdded)
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

func TestRateLimitDelayHonorsServerHeaders(t *testing.T) {
	now := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.UTC)

	if got, source := rateLimitDelay(http.Header{"Retry-After": []string{"3"}}, 0, now); got < 3*time.Second+minRateLimitJitter || got > 3*time.Second+maxRateLimitJitter || source != "Retry-After" {
		t.Fatalf("rateLimitDelay() = %s, %q; want 3s plus 750ms-3s jitter", got, source)
	}

	reset := now.Add(17 * time.Second).Unix()
	resetHeaders := make(http.Header)
	resetHeaders.Set("X-RateLimit-Reset", fmt.Sprint(reset))
	if got, source := rateLimitDelay(resetHeaders, 0, now); got < 17*time.Second+minRateLimitJitter || got > 17*time.Second+maxRateLimitJitter || source != "X-RateLimit-Reset" {
		t.Fatalf("rateLimitDelay() = %s, %q; want 17s plus 750ms-3s jitter", got, source)
	}

	if got, source := rateLimitDelay(nil, 0, now); got < time.Minute+minRateLimitJitter || got > time.Minute+maxRateLimitJitter || source != "fallback" {
		t.Fatalf("rateLimitDelay() fallback = %s, %q; want 1m plus 750ms-3s jitter", got, source)
	}
	if got, _ := rateLimitDelay(nil, 1, now); got < 2*time.Minute+minRateLimitJitter || got > 2*time.Minute+maxRateLimitJitter {
		t.Fatalf("rateLimitDelay() second fallback = %s, want 2m plus 750ms-3s jitter", got)
	}
}

func TestGetDataRetriesAfterRateLimit(t *testing.T) {
	oldAPIURL, oldAPIKey, oldHTTPClient := apiURL, apiKey, httpClient
	rateLimitCooldown.Lock()
	oldCooldown := rateLimitCooldown.until
	rateLimitCooldown.Unlock()
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
		rateLimitCooldown.Lock()
		rateLimitCooldown.until = oldCooldown
		rateLimitCooldown.Unlock()
	})

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("Too Many Requests"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	apiURL = server.URL
	apiKey = "test-key"
	httpClient = server.Client()
	rateLimitCooldown.Lock()
	rateLimitCooldown.until = time.Time{}
	rateLimitCooldown.Unlock()

	body, err := getData("search/metadata", http.MethodPost, "{}")
	if err != nil {
		t.Fatalf("getData() returned an error: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("getData() = %s, want successful retry response", body)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestNormalizeAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "https API URL", input: "https://immich.example/api", want: "https://immich.example/api/"},
		{name: "http API URL with trailing slash", input: "http://localhost:2283/api/", want: "http://localhost:2283/api/"},
		{name: "missing API path", input: "https://immich.example", wantErr: true},
		{name: "wrong API path", input: "https://immich.example/v1", wantErr: true},
		{name: "query string", input: "https://immich.example/api?key=value", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAPIURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeAPIURL(%q) error = %v, want error: %t", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("normalizeAPIURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateTakeoutPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Holiday", "metadata.json"), `{"title":"Holiday"}`)
	path, err := validateTakeoutPath(root)
	if err != nil {
		t.Fatalf("validateTakeoutPath() returned an error for a directory: %v", err)
	}
	if path != root {
		t.Fatalf("validateTakeoutPath() = %q, want %q", path, root)
	}

	file := filepath.Join(root, "not-a-directory")
	writeTestFile(t, file, "content")
	if _, err := validateTakeoutPath(file); err == nil {
		t.Fatal("validateTakeoutPath() accepted a file")
	}
	if _, err := validateTakeoutPath(filepath.Join(root, "missing")); err == nil {
		t.Fatal("validateTakeoutPath() accepted a missing path")
	}
	wrongRoot := t.TempDir()
	writeTestFile(t, filepath.Join(wrongRoot, "Google Photos", "photo.jpg"), "image bytes")
	if _, err := validateTakeoutPath(wrongRoot); err == nil {
		t.Fatal("validateTakeoutPath() accepted a directory without album metadata")
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
