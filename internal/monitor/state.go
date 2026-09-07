package monitor

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type State struct {
	mu   sync.Mutex
	path string
	Data StateData `json:"data"`
}

type StateData struct {
	LastCheck         time.Time      `json:"last_check"`
	SeenFiles         map[int]int64  `json:"seen_files"`       // fileID -> size
	SeenAssignments   map[int]bool   `json:"seen_assignments"` // assignmentID -> true
	SeenAnnouncements map[int]bool   `json:"seen_announcements"`
	AlertedDeadlines  map[int]string `json:"alerted_deadlines"` // assignmentID -> last alert level
}

func NewState(path string) *State {
	s := &State{path: path}
	s.Data.SeenFiles = make(map[int]int64)
	s.Data.SeenAssignments = make(map[int]bool)
	s.Data.SeenAnnouncements = make(map[int]bool)
	s.Data.AlertedDeadlines = make(map[int]string)
	return s
}

func (s *State) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(data, &s.Data)
}

func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0644)
}

func (s *State) IsFileNew(id int, size int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.Data.SeenFiles[id]
	return !ok || prev != size
}

func (s *State) MarkFile(id int, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Data.SeenFiles[id] = size
}

func (s *State) IsAnnouncementNew(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.Data.SeenAnnouncements[id]
}

func (s *State) MarkAnnouncement(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Data.SeenAnnouncements[id] = true
}

func (s *State) IsAssignmentNew(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.Data.SeenAssignments[id]
}

func (s *State) MarkAssignment(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Data.SeenAssignments[id] = true
}

func (s *State) ShouldAlertDeadline(id int, level string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Data.AlertedDeadlines[id] != level
}

func (s *State) MarkDeadlineAlert(id int, level string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Data.AlertedDeadlines[id] = level
}
