package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestReadFilesDiscoversAlbumSidecars(t *testing.T) {
	oldTakeout := takeout
	oldTakeoutPath := takeoutPath
	t.Cleanup(func() {
		takeout = oldTakeout
		takeoutPath = oldTakeoutPath
	})

	root := t.TempDir()
	albumPath := filepath.Join(root, "Auf Kreta mit Theo #2025")
	writeTestFile(t, filepath.Join(albumPath, "metadata.json"), `{"title":"Auf Kreta mit Theo #2025"}`)
	writeTestFile(t, filepath.Join(albumPath, "SNY00032_24MP.jpg.supplemental-metadata.json"), `{
  "title": "SNY00032_24MP.jpg",
  "creationTime": {"timestamp": "1760044090"},
  "photoTakenTime": {"timestamp": "1759504776"}
}`)

	takeout = nil
	takeoutPath = root
	readFiles(false)

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
	readAllDir(root)

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
	t.Cleanup(func() {
		apiURL, apiKey, httpClient = oldAPIURL, oldAPIKey, oldHTTPClient
	})

	var addedIDs []string
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
			t.Fatalf("looked up an asset that was already in the album")
		}
		_, _ = w.Write([]byte(`{"assets":{"total":1,"count":1,"items":[{"id":"new-id","originalFileName":"new.jpg"}]}}`))
	}))
	defer server.Close()

	apiURL = server.URL + "/"
	apiKey = "test-key"
	httpClient = server.Client()

	syncAlbum(Album{ID: "album-id", AlbumName: "Holiday"}, []TakeoutFile{
		{Filename: "already.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
		{Filename: "new.jpg", CreationTime: "1700000000", PhotoTakenTime: "1700000000"},
	}, true)

	if len(addedIDs) != 1 || addedIDs[0] != "new-id" {
		t.Fatalf("added asset IDs = %v, want [new-id]", addedIDs)
	}
}
