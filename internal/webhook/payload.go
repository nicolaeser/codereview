package webhook

import (
	"encoding/json"
	"strings"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

type payload struct {
	ObjectKind string      `json:"object_kind"`
	EventType  string      `json:"event_type"`
	EventName  string      `json:"event_name"`
	User       gitlab.User `json:"user"`
	ProjectID  int64       `json:"project_id"`
	Project    struct {
		ID int64 `json:"id"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int64  `json:"iid"`
		Action       string `json:"action"`
		State        string `json:"state"`
		Note         string `json:"note"`
		Body         string `json:"body"`
		NoteableType string `json:"noteable_type"`
		DiscussionID string `json:"discussion_id"`
		System       bool   `json:"system"`
	} `json:"object_attributes"`
	MergeRequest struct {
		ID  int64 `json:"id"`
		IID int64 `json:"iid"`
	} `json:"merge_request"`
}

func parsePayload(raw []byte) (payload, error) {
	var parsed payload
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return payload{}, err
	}
	return parsed, nil
}

func (p payload) kind() string {
	if kind := strings.ToLower(strings.TrimSpace(p.ObjectKind)); kind != "" {
		return kind
	}
	if kind := strings.ToLower(strings.TrimSpace(p.EventType)); kind != "" {
		return kind
	}
	return strings.ToLower(strings.TrimSpace(p.EventName))
}

func (p payload) projectID() int64 {
	if p.Project.ID != 0 {
		return p.Project.ID
	}
	return p.ProjectID
}

func (p payload) mergeRequestIID() int64 {
	if p.kind() == "note" {
		return p.MergeRequest.IID
	}
	return p.ObjectAttributes.IID
}

func (p payload) action() string {
	return strings.ToLower(strings.TrimSpace(p.ObjectAttributes.Action))
}

func (p payload) noteBody() string {
	if strings.TrimSpace(p.ObjectAttributes.Note) != "" {
		return p.ObjectAttributes.Note
	}
	return p.ObjectAttributes.Body
}

func (p payload) noteableType() string {
	return strings.ToLower(strings.TrimSpace(p.ObjectAttributes.NoteableType))
}

func (p payload) discussionID() string {
	return strings.TrimSpace(p.ObjectAttributes.DiscussionID)
}

func mergeRequestAction(action string) (run bool, reason string) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "open", "opened", "reopen", "reopened", "update", "updated", "sync", "synchronize":
		return true, ""
	case "merge", "merged", "close", "closed", "approved", "unapproved", "approval", "unapproval":
		return false, "merge request action skipped"
	default:
		return false, "unhandled merge request action"
	}
}
