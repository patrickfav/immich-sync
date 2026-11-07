package main

import (
	"os"
	"path/filepath"
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
