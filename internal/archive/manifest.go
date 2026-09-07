// Package archive mirrors Canvas course materials onto local disk, so a
// scheduled run accumulates every file the LMS exposes instead of the user
// clicking through each course by hand.
package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// ManifestName is the manifest file written at the root of the archive dir.
const ManifestName = "archive-manifest.json"

// Entry records one archived file, keyed by its Canvas file ID.
//
// The ID is the key on purpose: macOS stores Korean filenames in NFD while
// Canvas serves NFC, so any "does this name already exist on disk" check would
// mismatch and re-download the whole archive on every run.
type Entry struct {
	CourseID     int       `json:"course_id"`
	CourseName   string    `json:"course_name"`
	RelPath      string    `json:"relpath"`
	DisplayName  string    `json:"display_name"`
	Size         int64     `json:"size"`
	UpdatedAt    time.Time `json:"updated_at"`
	DownloadedAt time.Time `json:"downloaded_at"`
	Source       string    `json:"source"`
}

// Manifest is the archive's index. It is deliberately separate from
// monitor.State: that map tracks which files have been *announced* on Telegram,
// and sharing it would make an archiver skip every file the notifier saw first.
type Manifest struct {
	mu sync.Mutex

	path    string
	Version int              `json:"version"`
	Updated time.Time        `json:"updated_at"`
	Files   map[string]Entry `json:"files"`
}

func NewManifest(dir string) *Manifest {
	return &Manifest{
		path:    filepath.Join(dir, ManifestName),
		Version: 1,
		Files:   make(map[string]Entry),
	}
}

func (m *Manifest) Load() error {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var loaded struct {
		Version int              `json:"version"`
		Updated time.Time        `json:"updated_at"`
		Files   map[string]Entry `json:"files"`
	}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if loaded.Files != nil {
		m.Files = loaded.Files
	}
	if loaded.Version != 0 {
		m.Version = loaded.Version
	}
	m.Updated = loaded.Updated
	return nil
}

func (m *Manifest) Save() error {
	m.mu.Lock()
	snapshot := struct {
		Version int              `json:"version"`
		Updated time.Time        `json:"updated_at"`
		Files   map[string]Entry `json:"files"`
	}{Version: m.Version, Updated: time.Now().UTC(), Files: m.Files}
	m.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *Manifest) Get(fileID int) (Entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.Files[strconv.Itoa(fileID)]
	return e, ok
}

func (m *Manifest) Put(fileID int, e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Files[strconv.Itoa(fileID)] = e
}

// TakenPaths returns every relpath already claimed by a file other than
// exceptID, so a new file with a colliding display name can be disambiguated.
func (m *Manifest) TakenPaths() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.Files))
	for k, e := range m.Files {
		id, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		out[e.RelPath] = id
	}
	return out
}

func (m *Manifest) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Files)
}
