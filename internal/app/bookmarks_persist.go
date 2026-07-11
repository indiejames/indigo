package app

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type savedBookmark struct {
	FilePath string `json:"filePath"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Note     string `json:"note,omitempty"`
	Marker   string `json:"marker,omitempty"`
}

func bookmarksFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "indigo", "plugins", "bookmarks", "bookmarks.json"), nil
}

func loadSavedBookmarks() []bookmark {
	path, err := bookmarksFilePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var saved []savedBookmark
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil
	}
	bmarks := make([]bookmark, len(saved))
	for i, s := range saved {
		marker := s.Marker
		if marker == "" {
			marker = "▶"
		}
		bmarks[i] = bookmark{
			filePath: s.FilePath,
			line:     s.Line,
			col:      s.Col,
			note:     s.Note,
			marker:   marker,
			active:   true,
		}
	}
	return bmarks
}

func persistBookmarks(bmarks []bookmark) {
	path, err := bookmarksFilePath()
	if err != nil {
		return
	}
	var saved []savedBookmark
	for _, b := range bmarks {
		if b.active {
			saved = append(saved, savedBookmark{
				FilePath: b.filePath,
				Line:     b.line,
				Col:      b.col,
				Note:     b.note,
				Marker:   b.marker,
			})
		}
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)      //nolint:errcheck
	os.WriteFile(path, data, 0o644)              //nolint:errcheck
}
