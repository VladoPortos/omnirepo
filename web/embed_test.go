package web

import (
	"io/fs"
	"testing"
)

func TestDistFS_ContainsIndexHTML(t *testing.T) {
	f, err := DistFS.Open("dist/index.html")
	if err != nil {
		t.Fatalf("DistFS.Open(dist/index.html): %v", err)
	}
	f.Close()
}

func TestDistFS_ContainsSwaggerIndexHTML(t *testing.T) {
	f, err := DistFS.Open("dist/swagger/index.html")
	if err != nil {
		t.Fatalf("DistFS.Open(dist/swagger/index.html): %v", err)
	}
	f.Close()
}

func TestDistFS_ContainsFontFiles(t *testing.T) {
	fontDir := "dist/assets"
	entries, err := fs.ReadDir(DistFS, fontDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", fontDir, err)
	}

	var woff2Count int
	for _, e := range entries {
		if !e.IsDir() {
			name := e.Name()
			if len(name) > 6 && name[len(name)-6:] == ".woff2" {
				woff2Count++
			}
		}
	}
	if woff2Count < 3 {
		t.Errorf("expected at least 3 .woff2 font files in dist/assets, got %d", woff2Count)
	}
}

func TestDistFS_ContainsHashedAssets(t *testing.T) {
	entries, err := fs.ReadDir(DistFS, "dist/assets")
	if err != nil {
		t.Fatalf("ReadDir(dist/assets): %v", err)
	}

	var jsCount, cssCount int
	for _, e := range entries {
		name := e.Name()
		n := len(name)
		if n > 3 && name[n-3:] == ".js" {
			jsCount++
		}
		if n > 4 && name[n-4:] == ".css" {
			cssCount++
		}
	}
	if jsCount == 0 {
		t.Error("expected at least one .js file in dist/assets")
	}
	if cssCount == 0 {
		t.Error("expected at least one .css file in dist/assets")
	}
}
