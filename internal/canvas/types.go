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
	ID             int        `json:"id"`
	CourseID       int        `json:"course_id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	DueAt          *time.Time `json:"due_at"`
	PointsPossible float64    `json:"points_possible"`
	HTMLURL        string     `json:"html_url"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Submitted      bool       `json:"has_submitted_submissions"`
}

type File struct {
	ID          int       `json:"id"`
	FolderID    int       `json:"folder_id"`
	DisplayName string    `json:"display_name"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content-type"`
	MimeClass   string    `json:"mime_class"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ModifiedAt  time.Time `json:"modified_at"`
	// LockedForUser marks a file the enrollment cannot read yet. Canvas still
	// lists it, but returns no usable URL, so it must be skipped rather than
	// downloaded as a zero-byte stub.
	LockedForUser bool `json:"locked_for_user"`
	HiddenForUser bool `json:"hidden_for_user"`
}

// Folder is a Canvas course folder, used to rebuild the on-disk directory
// layout the user sees in the LMS.
type Folder struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	FullName       string `json:"full_name"`
	ParentFolderID *int   `json:"parent_folder_id"`
	FilesCount     int    `json:"files_count"`
	Hidden         bool   `json:"hidden"`
	ForSubmissions bool   `json:"for_submissions"`
}

type Announcement struct {
	ID          int       `json:"id"`
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	PostedAt    time.Time `json:"posted_at"`
	HTMLURL     string    `json:"html_url"`
	UserName    string    `json:"user_name"`
	ContextCode string    `json:"context_code"`
}

type Module struct {
	ID       int          `json:"id"`
	Name     string       `json:"name"`
	Position int          `json:"position"`
	Items    []ModuleItem `json:"items"`
}

type ModuleItem struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	ContentID   int    `json:"content_id"`
	HTMLURL     string `json:"html_url"`
	ExternalURL string `json:"external_url,omitempty"`
}

// Submission is the student's own work on one assignment.
type Submission struct {
	ID             int                 `json:"id"`
	CourseID       int                 `json:"-"`
	AssignmentID   int                 `json:"assignment_id"`
	Score          *float64            `json:"score"`
	Grade          string              `json:"grade"`
	SubmittedAt    *time.Time          `json:"submitted_at"`
	GradedAt       *time.Time          `json:"graded_at"`
	SubmissionType string              `json:"submission_type"`
	Body           string              `json:"body"`
	URL            string              `json:"url"`
	Attempt        int                 `json:"attempt"`
	Late           bool                `json:"late"`
	Missing        bool                `json:"missing"`
	Excused        bool                `json:"excused"`
	Attachments    []File              `json:"attachments"`
	Comments       []SubmissionComment `json:"submission_comments"`
	Assignment     *Assignment         `json:"assignment"`
}

// SubmissionComment is instructor (or peer) feedback on a submission.
type SubmissionComment struct {
	ID         int       `json:"id"`
	AuthorName string    `json:"author_name"`
	Comment    string    `json:"comment"`
	CreatedAt  time.Time `json:"created_at"`
}

// Grades is the student's standing in a course.
type Grades struct {
	HTMLURL      string   `json:"html_url"`
	CurrentScore *float64 `json:"current_score"`
	FinalScore   *float64 `json:"final_score"`
	CurrentGrade string   `json:"current_grade"`
	FinalGrade   string   `json:"final_grade"`
}
