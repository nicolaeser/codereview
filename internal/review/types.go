package review

import "github.com/nicolaeser/codereview/internal/gitlab"

// Job describes a single one-shot merge-request review or mention answer.
type Job struct {
	ProjectID    int64
	MergeRequest int64
	RequestID    string
	Full         bool
	SummaryOnly  bool
	Force        bool
	Reason       string
	Mode         string
	// ExtraFocus is untrusted leftover text from an @mention. It may steer a
	// review or supply an ask question; it must never replace trusted protocol.
	ExtraFocus string
	// Ask answers the ExtraFocus question in-thread instead of running a review.
	Ask bool
	// DiscussionID is the GitLab discussion to reply in for ask/help answers.
	DiscussionID string
}

type ReviewMode string

const (
	ModeQuick    ReviewMode = "quick"
	ModeStandard ReviewMode = "standard"
	ModeDeep     ReviewMode = "deep"
	ModeSecurity ReviewMode = "security"
)

func EffectiveReviewMode(requested, fallback string) ReviewMode {
	value := requested
	if value == "" {
		value = fallback
	}
	switch ReviewMode(value) {
	case ModeQuick, ModeDeep, ModeSecurity:
		return ReviewMode(value)
	default:
		return ModeStandard
	}
}

type Finding struct {
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	EndLine    int     `json:"end_line,omitempty"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Suggestion string  `json:"suggestion,omitempty"`
	Confidence float64 `json:"confidence"`
	LineText   string  `json:"-"`
}

type WalkthroughItem struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
	Risk    string `json:"risk"`
}

type AIReview struct {
	Summary                string            `json:"summary"`
	Walkthrough            []WalkthroughItem `json:"walkthrough"`
	IssueAssessments       []IssueAssessment `json:"issue_assessments,omitempty"`
	Findings               []Finding         `json:"findings"`
	AgentReport            string            `json:"agent_report,omitempty"`
	BranchName             string            `json:"branch_name,omitempty"`
	CodeAdaptationRequired *bool             `json:"code_adaptation_required,omitempty"`
}

type IssueAssessment struct {
	IID     int64  `json:"iid"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type PreparedDiff struct {
	Diff         gitlab.Diff
	ChangedLines map[int]struct{}
	// AddedLines is the text of each '+' line keyed by new-file number, without the leading '+'.
	AddedLines map[int]string
}

type RepositoryContext struct {
	Tree   []gitlab.TreeEntry
	Files  map[string]string
	Issues []gitlab.Issue
}

type ReviewDetails struct {
	Mode               ReviewMode
	Provider           string
	EURouting          bool
	Files              int
	DiffChars          int
	ContextFiles       int
	TreeEntries        int
	LinkedIssues       int
	ModelBatches       int
	Verification       bool
	SuppressedFindings int
	OpenPriorFindings  int
}
