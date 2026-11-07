package main

import (
	"path/filepath"
	"testing"
)

func TestLocalTakeoutSourceReadAlbumsAdaptsTakeoutData(t *testing.T) {
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
	writeTestFile(t, filepath.Join(root, "Holiday", "metadata.json"), `{"title":"Holiday","description":"A trip","date":{"timestamp":"1760044090"}}`)
	writeTestFile(t, filepath.Join(root, "Holiday", "photo.jpg.supplemental-metadata.json"), `{"title":"photo.jpg","creationTime":{"timestamp":"1760044090"},"photoTakenTime":{"timestamp":"1759504776"}}`)
	takeoutPath = root

	data, err := (localTakeoutSource{}).ReadAlbums()
	if err != nil {
		t.Fatalf("localTakeoutSource.ReadAlbums() returned an error: %v", err)
	}
	if len(data.Albums["Holiday"]) != 1 {
		t.Fatalf("adapted album files = %d, want 1", len(data.Albums["Holiday"]))
	}
	if data.Descriptions["Holiday"] != "A trip" {
		t.Fatalf("adapted description = %q, want A trip", data.Descriptions["Holiday"])
	}
	if data.CreationTimes["Holiday"] != 1760044090 {
		t.Fatalf("adapted creation time = %d, want 1760044090", data.CreationTimes["Holiday"])
	}
}
