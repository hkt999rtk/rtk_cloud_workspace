package main

import (
	"sort"
	"time"
)

type Repository struct {
	Owner       string    `json:"owner"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	IsSubmodule bool      `json:"isSubmodule"`
	Stale       bool      `json:"stale"`
	Error       string    `json:"error,omitempty"`
	LastSync    time.Time `json:"lastSync,omitempty"`
}

type Actor struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatarUrl,omitempty"`
}

type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
}

type JobSummary struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

type Card struct {
	Key             string       `json:"key"`
	Kind            string       `json:"kind"`
	Owner           string       `json:"owner"`
	Repo            string       `json:"repo"`
	IsSubmodule     bool         `json:"isSubmodule"`
	RunID           int64        `json:"runId"`
	RunNumber       int64        `json:"runNumber"`
	Attempt         int          `json:"attempt"`
	WorkflowID      int64        `json:"workflowId"`
	Workflow        string       `json:"workflow"`
	JobID           int64        `json:"jobId,omitempty"`
	JobName         string       `json:"jobName,omitempty"`
	RunnerName      string       `json:"runnerName,omitempty"`
	RunnerLabels    []string     `json:"runnerLabels,omitempty"`
	DisplayTitle    string       `json:"displayTitle"`
	Topic           string       `json:"topic"`
	Event           string       `json:"event"`
	Status          string       `json:"status"`
	Conclusion      string       `json:"conclusion,omitempty"`
	HeadBranch      string       `json:"headBranch,omitempty"`
	HeadSHA         string       `json:"headSha,omitempty"`
	Actor           Actor        `json:"actor"`
	TriggeringActor Actor        `json:"triggeringActor"`
	PR              *PullRequest `json:"pr,omitempty"`
	CreatedAt       time.Time    `json:"createdAt"`
	RunStartedAt    time.Time    `json:"runStartedAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
	URL             string       `json:"url"`
	Jobs            JobSummary   `json:"jobs"`
	RepoStale       bool         `json:"repoStale"`
}

type RateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt,omitempty"`
}

type Snapshot struct {
	GeneratedAt    time.Time    `json:"generatedAt"`
	LastSuccessful time.Time    `json:"lastSuccessfulSync,omitempty"`
	NextRefresh    time.Time    `json:"nextRefreshAt,omitempty"`
	CompletedLimit int          `json:"completedLimit"`
	RateLimit      RateLimit    `json:"rateLimit"`
	Repositories   []Repository `json:"repositories"`
	Queued         []Card       `json:"queued"`
	Running        []Card       `json:"running"`
	Completed      []Card       `json:"completed"`
}

type Step struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type Job struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	URL         string     `json:"url,omitempty"`
	RunnerName  string     `json:"runnerName,omitempty"`
	Labels      []string   `json:"labels,omitempty"`
	Steps       []Step     `json:"steps"`
}

type Attempt struct {
	Number     int       `json:"number"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt,omitempty"`
	Jobs       []Job     `json:"jobs"`
}

type RunDetail struct {
	Card     Card      `json:"card"`
	Attempts []Attempt `json:"attempts"`
}

func started(c Card) time.Time {
	if !c.RunStartedAt.IsZero() {
		return c.RunStartedAt
	}
	return c.CreatedAt
}

func finished(c Card) time.Time {
	if !c.UpdatedAt.IsZero() {
		return c.UpdatedAt
	}
	return started(c)
}

func arrangeCards(cards []Card, completedLimit int) (queued, running, completed []Card) {
	for _, card := range cards {
		switch card.Status {
		case "queued", "requested", "waiting", "pending":
			queued = append(queued, card)
		case "in_progress":
			running = append(running, card)
		case "completed":
			completed = append(completed, card)
		default:
			queued = append(queued, card)
		}
	}
	sort.SliceStable(queued, func(i, j int) bool { return queued[i].CreatedAt.Before(queued[j].CreatedAt) })
	sort.SliceStable(running, func(i, j int) bool { return started(running[i]).Before(started(running[j])) })
	sort.SliceStable(completed, func(i, j int) bool {
		a, b := finished(completed[i]), finished(completed[j])
		if a.Equal(b) {
			if completed[i].RunID == completed[j].RunID {
				return completed[i].JobID > completed[j].JobID
			}
			return completed[i].RunID > completed[j].RunID
		}
		return a.After(b)
	})
	if len(completed) > completedLimit {
		completed = completed[:completedLimit]
	}
	return
}
