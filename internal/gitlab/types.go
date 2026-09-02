package gitlab

import "strings"

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Bot      bool   `json:"bot"`
}

type Label struct {
	Name string `json:"name"`
}

type DiffRefs struct {
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
}

type MergeRequestRefs struct {
	Full string `json:"full"`
}

type MergeRequest struct {
	ID             int64            `json:"id"`
	IID            int64            `json:"iid"`
	ProjectID      int64            `json:"project_id"`
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	State          string           `json:"state"`
	Draft          bool             `json:"draft"`
	SourceBranch   string           `json:"source_branch"`
	TargetBranch   string           `json:"target_branch"`
	SHA            string           `json:"sha"`
	WebURL         string           `json:"web_url"`
	Author         User             `json:"author"`
	Reviewers      []User           `json:"reviewers"`
	Labels         []string         `json:"labels"`
	References     MergeRequestRefs `json:"references"`
	DiffRefs       DiffRefs         `json:"diff_refs"`
	ChangesCount   string           `json:"changes_count"`
	DetailedStatus string           `json:"detailed_merge_status"`
}

type Diff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	AMode       string `json:"a_mode"`
	BMode       string `json:"b_mode"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Generated   bool   `json:"generated_file"`
	Collapsed   bool   `json:"collapsed"`
	TooLarge    bool   `json:"too_large"`
}

type CompareResult struct {
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
	Diffs []Diff `json:"diffs"`
}

type DiffVersion struct {
	ID             int64  `json:"id"`
	HeadCommitSHA  string `json:"head_commit_sha"`
	BaseCommitSHA  string `json:"base_commit_sha"`
	StartCommitSHA string `json:"start_commit_sha"`
}

type TreeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

type Issue struct {
	IID         int64    `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	WebURL      string   `json:"web_url"`
}

type Note struct {
	ID         int64         `json:"id"`
	Body       string        `json:"body"`
	Resolvable bool          `json:"resolvable"`
	Resolved   bool          `json:"resolved"`
	Author     User          `json:"author"`
	Position   *DiffPosition `json:"position,omitempty"`
}

type Discussion struct {
	ID         string `json:"id"`
	Individual bool   `json:"individual_note"`
	Notes      []Note `json:"notes"`
}

type DiffPosition struct {
	PositionType string `json:"position_type"`
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	NewLine      int    `json:"new_line,omitempty"`
	OldLine      int    `json:"old_line,omitempty"`
}

type MergeRequestApprovals struct {
	Approved   bool `json:"approved"`
	ApprovedBy []struct {
		User User `json:"user"`
	} `json:"approved_by"`
}

type Todo struct {
	ID         int64  `json:"id"`
	ActionName string `json:"action_name"`
	TargetType string `json:"target_type"`
	TargetURL  string `json:"target_url"`
	Body       string `json:"body"`
	State      string `json:"state"`
	Project    struct {
		ID int64 `json:"id"`
	} `json:"project"`
	Target struct {
		ID        int64 `json:"id"`
		IID       int64 `json:"iid"`
		ProjectID int64 `json:"project_id"`
	} `json:"target"`
}

// IsAutomationUser reports GitLab project/group bots that cannot approve or
// receive @mention todos the way a human user can.
func IsAutomationUser(user User) bool {
	if user.Bot {
		return true
	}
	name := strings.ToLower(user.Username)
	return strings.Contains(name, "_bot_") || strings.HasSuffix(name, "_bot")
}
