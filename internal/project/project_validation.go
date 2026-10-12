package project

import (
	"fmt"
	"strings"
)

func ValidateCreateProjectRequest(req CreateProjectRequest) (*CreateProjectRequest, *DomainError) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, InvalidArgument("title is required")
	}
	origin := strings.TrimSpace(req.Origin)
	if origin == "" {
		return nil, InvalidArgument("origin is required")
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		return nil, InvalidArgument("goal is required")
	}
	hints := make([]CreateHintInline, len(req.Hints))
	for index, hint := range req.Hints {
		content := strings.TrimSpace(hint.Content)
		if content == "" {
			return nil, InvalidArgument(fmt.Sprintf("hints[%d].content is required", index))
		}
		hints[index] = CreateHintInline{Content: content}
	}
	return &CreateProjectRequest{
		Title: title, Origin: origin, Goal: goal,
		BootstrapEnabled: req.BootstrapEnabled, Hints: hints,
	}, nil
}

func ValidateGoalNotInSources(factIDs []string) error {
	for _, factID := range factIDs {
		if factID == "goal" {
			return fmt.Errorf("goal cannot be used in from")
		}
	}
	return nil
}

func ValidateCompleteRequest(req CompleteRequest) (*CompleteRequest, *DomainError) {
	description := strings.TrimSpace(req.Description)
	if description == "" {
		return nil, InvalidArgument("description is required")
	}
	if len(req.From) == 0 {
		return nil, InvalidArgument("from is required and must not be empty")
	}
	if req.ExpectedRevision < 0 {
		return nil, InvalidArgument("expected_revision must be a non-negative integer")
	}
	return &CompleteRequest{Description: description, From: req.From, ExpectedRevision: req.ExpectedRevision}, nil
}

func ValidateCreateHintRequest(req CreateHintRequest) (*CreateHintRequest, *DomainError) {
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, InvalidArgument("content is required")
	}
	if req.ExpectedRevision < 0 {
		return nil, InvalidArgument("expected_revision must be a non-negative integer")
	}
	return &CreateHintRequest{Content: content, ExpectedRevision: req.ExpectedRevision}, nil
}

func ValidateReopenRequest(req ReopenRequest) (*ReopenRequest, *DomainError) {
	feedback := strings.TrimSpace(req.Feedback)
	if feedback == "" {
		return nil, InvalidArgument("feedback is required")
	}
	if req.ExpectedRevision < 0 {
		return nil, InvalidArgument("expected_revision must be a non-negative integer")
	}
	return &ReopenRequest{Feedback: feedback, ExpectedRevision: req.ExpectedRevision}, nil
}
