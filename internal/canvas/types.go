package canvas

import "time"

type Course struct {
	ID               int        `json:"id"`
	Name             string     `json:"name"`
	CourseCode       string     `json:"course_code"`
	EnrollmentTermID int        `json:"enrollment_term_id"`
	StartAt          *time.Time `json:"start_at,omitempty"`
	EndAt            *time.Time `json:"end_at,omitempty"`
}

type Assignment struct {
	ID          int        `json:"id"`
	CourseID    int        `json:"course_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	DueAt       *time.Time `json:"due_at"`
	PointsPossible float64 `json:"points_possible"`
	HTMLURL     string     `json:"html_url"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Submitted   bool       `json:"has_submitted_submissions"`
}

type File struct {
	ID          int       `json:"id"`
	FolderID    int       `json:"folder_id"`
	DisplayName string    `json:"display_name"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content-type"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Announcement struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	PostedAt  time.Time `json:"posted_at"`
	HTMLURL   string    `json:"html_url"`
	UserName  string    `json:"user_name"`
	ContextCode string `json:"context_code"`
}

type Module struct {
	ID       int          `json:"id"`
	Name     string       `json:"name"`
	Position int          `json:"position"`
	Items    []ModuleItem `json:"items"`
}

type ModuleItem struct {
	ID         int    `json:"id"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	ContentID  int    `json:"content_id"`
	HTMLURL    string `json:"html_url"`
	ExternalURL string `json:"external_url,omitempty"`
}
