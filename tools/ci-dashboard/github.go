package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type githubClient struct {
	baseURL string
	token   string
	http    *http.Client
	onRate  func(http.Header)
}

type repoState struct {
	repo             Repository
	etag             string
	runs             []Card
	openPRs          []OpenPullRequest
	jobs             map[int64]JobSummary
	lastSync         time.Time
	lastAttempt      time.Time
	err              string
	backoffUntil     time.Time
	backoff          time.Duration
	completedJobs    map[int64][]Job
	pullLastSync     time.Time
	pullLastAttempt  time.Time
	pullErr          string
	pullBackoffUntil time.Time
	pullBackoff      time.Duration
}

type poller struct {
	mu             sync.RWMutex
	client         *githubClient
	repos          map[string]*repoState
	order          []string
	interval       time.Duration
	completedLimit int
	rate           RateLimit
	lastSuccessful time.Time
	nextRefresh    time.Time
	prCache        map[string]PullRequest
	lastClient     time.Time
	clientIdle     time.Duration
	wake           chan struct{}
}

const defaultClientIdleTimeout = 15 * time.Second

type workflowRunsResponse struct {
	Runs []workflowRun `json:"workflow_runs"`
}

type workflowRun struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	DisplayTitle    string    `json:"display_title"`
	Event           string    `json:"event"`
	Status          string    `json:"status"`
	Conclusion      string    `json:"conclusion"`
	WorkflowID      int64     `json:"workflow_id"`
	RunNumber       int64     `json:"run_number"`
	RunAttempt      int       `json:"run_attempt"`
	HeadBranch      string    `json:"head_branch"`
	HeadSHA         string    `json:"head_sha"`
	HTMLURL         string    `json:"html_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	RunStartedAt    time.Time `json:"run_started_at"`
	Actor           apiActor  `json:"actor"`
	TriggeringActor apiActor  `json:"triggering_actor"`
	PullRequests    []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

type apiActor struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type pullResponse struct {
	Number  int      `json:"number"`
	State   string   `json:"state"`
	Title   string   `json:"title"`
	HTMLURL string   `json:"html_url"`
	Draft   bool     `json:"draft"`
	User    apiActor `json:"user"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type jobsResponse struct {
	Jobs []apiJob `json:"jobs"`
}

type apiJob struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	HTMLURL     string     `json:"html_url"`
	RunnerName  string     `json:"runner_name"`
	Labels      []string   `json:"labels"`
	Steps       []struct {
		Number      int        `json:"number"`
		Name        string     `json:"name"`
		Status      string     `json:"status"`
		Conclusion  string     `json:"conclusion"`
		StartedAt   *time.Time `json:"started_at"`
		CompletedAt *time.Time `json:"completed_at"`
	} `json:"steps"`
}

type attemptResponse struct {
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	RunAttempt   int       `json:"run_attempt"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func newPoller(client *githubClient, repos []Repository, interval time.Duration) *poller {
	p := &poller{
		client:         client,
		repos:          make(map[string]*repoState),
		interval:       interval,
		completedLimit: 20,
		prCache:        make(map[string]PullRequest),
		clientIdle:     defaultClientIdleTimeout,
		wake:           make(chan struct{}, 1),
	}
	client.onRate = p.captureRate
	for _, repo := range repos {
		key := repo.Owner + "/" + repo.Name
		p.order = append(p.order, key)
		p.repos[key] = &repoState{repo: repo, jobs: make(map[int64]JobSummary), completedJobs: make(map[int64][]Job)}
	}
	return p
}

func (p *poller) run(ctx context.Context) {
	timer := time.NewTimer(p.interval)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var next <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			if p.clientActiveAt(time.Now()) {
				p.refresh(ctx)
				resetTimer(timer, p.interval)
				next = timer.C
			}
		case now := <-next:
			next = nil
			if p.clientActiveAt(now) {
				p.refresh(ctx)
				timer.Reset(p.interval)
				next = timer.C
			}
		}
	}
}

func resetTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
}

func (p *poller) recordClientActivity() {
	p.recordClientActivityAt(time.Now())
}

func (p *poller) recordClientActivityAt(now time.Time) {
	p.mu.Lock()
	wasActive := !p.lastClient.IsZero() && now.Sub(p.lastClient) <= p.clientIdle
	p.lastClient = now
	p.mu.Unlock()
	if !wasActive {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *poller) clientActiveAt(now time.Time) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.lastClient.IsZero() && now.Sub(p.lastClient) <= p.clientIdle
}

func (p *poller) refresh(ctx context.Context) {
	now := time.Now()
	anySuccess := false
	for _, key := range p.order {
		p.mu.RLock()
		state := p.repos[key]
		backoffUntil := state.backoffUntil
		pullBackoffUntil := state.pullBackoffUntil
		p.mu.RUnlock()
		if !now.Before(backoffUntil) {
			if err := p.refreshRepo(ctx, key); err != nil {
				p.recordFailure(key, err)
			}
		} else {
			p.mu.RLock()
			p.lastSuccessful = maxTime(p.lastSuccessful, state.lastSync)
			p.mu.RUnlock()
		}
		if now.Before(pullBackoffUntil) {
			continue
		}
		if err := p.refreshPullRequests(ctx, key); err != nil {
			p.recordPullFailure(key, err)
			continue
		}
		anySuccess = true
	}
	if anySuccess {
		p.mu.Lock()
		p.lastSuccessful = time.Now()
		p.nextRefresh = time.Now().Add(p.interval)
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		p.nextRefresh = time.Now().Add(p.interval)
		p.mu.Unlock()
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (p *poller) refreshPullRequests(ctx context.Context, key string) error {
	p.mu.RLock()
	repo := p.repos[key].repo
	p.mu.RUnlock()

	prs, err := p.listOpenPullRequests(ctx, repo)
	if err != nil {
		return err
	}
	p.mu.Lock()
	state := p.repos[key]
	state.openPRs = prs
	state.pullLastAttempt = time.Now()
	state.pullLastSync = state.pullLastAttempt
	state.pullErr = ""
	state.pullBackoff = 0
	state.pullBackoffUntil = time.Time{}
	for _, pr := range prs {
		p.prCache[fmt.Sprintf("%s/%s/%d", pr.Owner, pr.Repo, pr.Number)] = PullRequest{Number: pr.Number, Title: pr.Title, URL: pr.URL}
	}
	p.mu.Unlock()
	return nil
}

func (p *poller) listOpenPullRequests(ctx context.Context, repo Repository) ([]OpenPullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&sort=created&direction=asc&per_page=100", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var all []OpenPullRequest
	for path != "" {
		var raw []pullResponse
		next, err := p.client.getJSONPage(ctx, path, &raw)
		if err != nil {
			return nil, err
		}
		for _, item := range raw {
			if item.State != "" && item.State != "open" {
				continue
			}
			all = append(all, OpenPullRequest{Key: fmt.Sprintf("%s/%s/%d", repo.Owner, repo.Name, item.Number), Owner: repo.Owner, Repo: repo.Name, Number: item.Number, Title: item.Title, URL: item.HTMLURL, Author: Actor{Login: item.User.Login, AvatarURL: item.User.AvatarURL}, Draft: item.Draft, HeadBranch: item.Head.Ref, BaseBranch: item.Base.Ref, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
		}
		path = next
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		if all[i].Owner != all[j].Owner {
			return all[i].Owner < all[j].Owner
		}
		if all[i].Repo != all[j].Repo {
			return all[i].Repo < all[j].Repo
		}
		return all[i].Number < all[j].Number
	})
	return all, nil
}

func (p *poller) recordPullFailure(key string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.repos[key]
	state.pullLastAttempt = time.Now()
	state.pullErr = sanitizeError(err)
	if state.pullBackoff == 0 {
		state.pullBackoff = 15 * time.Second
	} else {
		state.pullBackoff *= 2
		if state.pullBackoff > 5*time.Minute {
			state.pullBackoff = 5 * time.Minute
		}
	}
	var retryErr *retryAfterError
	if errors.As(err, &retryErr) && retryErr.until.After(time.Now()) {
		state.pullBackoffUntil = retryErr.until
	} else {
		state.pullBackoffUntil = time.Now().Add(state.pullBackoff)
	}
}

func (p *poller) refreshRepo(ctx context.Context, key string) error {
	p.mu.RLock()
	state := p.repos[key]
	etag := state.etag
	for _, card := range state.runs {
		// The workflow-runs ETag can remain unchanged while GitHub assigns a
		// queued job to a runner or advances its status. Keep polling active
		// jobs until their run-level snapshot is complete. Run-level fallback
		// cards also need another attempt to load their jobs.
		if card.Status != "completed" || card.Kind != "job" {
			etag = ""
			break
		}
	}
	repo := state.repo
	p.mu.RUnlock()

	var response workflowRunsResponse
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=100", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	newETag, notModified, err := p.client.getJSON(ctx, path, etag, &response)
	if err != nil {
		return err
	}
	if notModified {
		p.mu.Lock()
		state.lastAttempt = time.Now()
		state.lastSync = time.Now()
		state.err = ""
		state.backoff = 0
		state.backoffUntil = time.Time{}
		p.lastSuccessful = state.lastSync
		p.mu.Unlock()
		return nil
	}

	cards := make([]Card, 0, len(response.Runs))
	completedRuns := 0
	for i, run := range response.Runs {
		card := cardFromRun(repo, run)
		if len(run.PullRequests) > 0 {
			card.PR = &PullRequest{Number: run.PullRequests[0].Number, Title: run.DisplayTitle}
			if i < 10 || run.Status != "completed" {
				if pr, prErr := p.pullRequest(ctx, repo.Owner, repo.Name, card.PR.Number); prErr == nil {
					card.PR = &pr
				}
			}
		}
		if run.Status != "completed" {
			if jobs, jobsErr := p.client.listJobs(ctx, repo.Owner, repo.Name, run.ID, 0); jobsErr == nil && len(jobs) > 0 {
				card.Jobs = summarizeJobs(jobs)
				occurrences := make(map[string]int)
				for _, job := range jobs {
					occurrences[job.Name]++
					cards = append(cards, cardFromJob(card, job, occurrences[job.Name]))
				}
				continue
			}
		}
		if run.Status == "completed" {
			completedRuns++
			expand := completedRuns <= 10
			if expand {
				jobs, jobsErr := p.completedRunJobs(ctx, repo.Owner, repo.Name, run.ID)
				if jobsErr == nil && len(jobs) > 0 {
					occurrences := make(map[string]int)
					for _, job := range jobs {
						occurrences[job.Name]++
						cards = append(cards, cardFromJob(card, job, occurrences[job.Name]))
					}
					continue
				}
				// Preserve a run-level fallback if GitHub temporarily cannot return
				// its jobs; the next successful poll replaces it with job cards.
				cards = append(cards, card)
			}
			continue
		}
		cards = append(cards, card)
	}

	p.mu.Lock()
	state.etag = newETag
	state.runs = cards
	state.lastAttempt = time.Now()
	state.lastSync = time.Now()
	state.err = ""
	state.backoff = 0
	state.backoffUntil = time.Time{}
	p.lastSuccessful = state.lastSync
	p.mu.Unlock()
	return nil
}

func (p *poller) completedRunJobs(ctx context.Context, owner, repo string, runID int64) ([]Job, error) {
	key := owner + "/" + repo
	p.mu.RLock()
	state := p.repos[key]
	cached, ok := state.completedJobs[runID]
	p.mu.RUnlock()
	if ok {
		return cached, nil
	}
	jobs, err := p.client.listJobs(ctx, owner, repo, runID, 0)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	state.completedJobs[runID] = jobs
	p.mu.Unlock()
	return jobs, nil
}

func (p *poller) recordFailure(key string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.repos[key]
	state.lastAttempt = time.Now()
	state.err = sanitizeError(err)
	if state.backoff == 0 {
		state.backoff = 15 * time.Second
	} else {
		state.backoff *= 2
		if state.backoff > 5*time.Minute {
			state.backoff = 5 * time.Minute
		}
	}
	var retryErr *retryAfterError
	if errors.As(err, &retryErr) && retryErr.until.After(time.Now()) {
		state.backoffUntil = retryErr.until
	} else {
		state.backoffUntil = time.Now().Add(state.backoff)
	}
}

func (p *poller) snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	snapshot := Snapshot{GeneratedAt: now, LastSuccessful: p.lastSuccessful, NextRefresh: p.nextRefresh, CompletedLimit: p.completedLimit, RateLimit: p.rate}
	var cards []Card
	for _, key := range p.order {
		state := p.repos[key]
		repo := state.repo
		repo.LastSync = state.lastSync
		repo.Error = state.err
		if state.pullErr != "" {
			if repo.Error != "" {
				repo.Error += "; "
			}
			repo.Error += "open PRs: " + state.pullErr
		}
		repo.Stale = state.err != "" || state.pullErr != "" || state.lastSync.IsZero() || state.pullLastSync.IsZero()
		snapshot.Repositories = append(snapshot.Repositories, repo)
		for _, card := range state.runs {
			card.RepoStale = repo.Stale
			cards = append(cards, card)
		}
		for _, pr := range state.openPRs {
			pr.RepoStale = repo.Stale
			snapshot.OpenPullRequests = append(snapshot.OpenPullRequests, pr)
		}
	}
	snapshot.Queued, snapshot.Running, snapshot.Completed = arrangeCards(cards, p.completedLimit)
	if snapshot.Queued == nil {
		snapshot.Queued = []Card{}
	}
	if snapshot.Running == nil {
		snapshot.Running = []Card{}
	}
	if snapshot.Completed == nil {
		snapshot.Completed = []Card{}
	}
	if snapshot.OpenPullRequests == nil {
		snapshot.OpenPullRequests = []OpenPullRequest{}
	}
	sort.SliceStable(snapshot.OpenPullRequests, func(i, j int) bool {
		a, b := snapshot.OpenPullRequests[i], snapshot.OpenPullRequests[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Number < b.Number
	})
	return snapshot
}

func (p *poller) pullRequest(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	key := fmt.Sprintf("%s/%s/%d", owner, repo, number)
	p.mu.RLock()
	cached, ok := p.prCache[key]
	p.mu.RUnlock()
	if ok {
		return cached, nil
	}
	var raw pullResponse
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	if _, _, err := p.client.getJSON(ctx, path, "", &raw); err != nil {
		return PullRequest{}, err
	}
	pr := PullRequest{Number: raw.Number, Title: raw.Title, URL: raw.HTMLURL}
	p.mu.Lock()
	p.prCache[key] = pr
	p.mu.Unlock()
	return pr, nil
}

func (p *poller) detail(ctx context.Context, owner, repo string, runID int64) (RunDetail, error) {
	key := owner + "/" + repo
	p.mu.RLock()
	state, ok := p.repos[key]
	var card Card
	if ok {
		for _, candidate := range state.runs {
			if candidate.RunID == runID {
				card = candidate
				break
			}
		}
	}
	p.mu.RUnlock()
	if !ok || card.RunID == 0 {
		return RunDetail{}, errors.New("run not found in dashboard snapshot")
	}
	detail := RunDetail{Card: card}
	for number := 1; number <= card.Attempt; number++ {
		var raw attemptResponse
		path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/attempts/%d", url.PathEscape(owner), url.PathEscape(repo), runID, number)
		if _, _, err := p.client.getJSON(ctx, path, "", &raw); err != nil {
			return RunDetail{}, fmt.Errorf("attempt %d: %w", number, err)
		}
		jobs, err := p.client.listJobs(ctx, owner, repo, runID, number)
		if err != nil {
			return RunDetail{}, fmt.Errorf("attempt %d jobs: %w", number, err)
		}
		detail.Attempts = append(detail.Attempts, Attempt{Number: number, Status: raw.Status, Conclusion: raw.Conclusion, StartedAt: raw.RunStartedAt, UpdatedAt: raw.UpdatedAt, Jobs: jobs})
	}
	return detail, nil
}

func cardFromRun(repo Repository, run workflowRun) Card {
	topic := strings.TrimSpace(run.DisplayTitle)
	if topic == "" || topic == run.Name {
		sha := run.HeadSHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		parts := []string{run.Name}
		if run.Actor.Login != "" {
			parts = append(parts, "@"+run.Actor.Login)
		}
		if run.HeadBranch != "" {
			parts = append(parts, run.HeadBranch)
		} else if sha != "" {
			parts = append(parts, sha)
		}
		topic = strings.Join(parts, " · ")
	}
	return Card{
		Key: repo.Owner + "/" + repo.Name + "/" + strconv.FormatInt(run.ID, 10), Kind: "run", Owner: repo.Owner, Repo: repo.Name,
		IsSubmodule: repo.IsSubmodule, RunID: run.ID, RunNumber: run.RunNumber, Attempt: max(run.RunAttempt, 1), WorkflowID: run.WorkflowID,
		Workflow: run.Name, DisplayTitle: run.DisplayTitle, Topic: topic, Event: run.Event, Status: run.Status, Conclusion: run.Conclusion,
		HeadBranch: run.HeadBranch, HeadSHA: run.HeadSHA, Actor: Actor{Login: run.Actor.Login, AvatarURL: run.Actor.AvatarURL},
		TriggeringActor: Actor{Login: run.TriggeringActor.Login, AvatarURL: run.TriggeringActor.AvatarURL}, CreatedAt: run.CreatedAt,
		RunStartedAt: run.RunStartedAt, UpdatedAt: run.UpdatedAt, URL: run.HTMLURL,
	}
}

func cardFromJob(parent Card, job Job, occurrence int) Card {
	card := parent
	card.Kind = "job"
	card.Key = fmt.Sprintf("%s/job/%s/%d", parent.Key, job.Name, occurrence)
	card.JobID = job.ID
	card.JobName = job.Name
	card.RunnerName = job.RunnerName
	card.RunnerLabels = append([]string(nil), job.Labels...)
	card.Status = job.Status
	card.Conclusion = job.Conclusion
	if job.StartedAt != nil {
		card.RunStartedAt = *job.StartedAt
	}
	if job.CompletedAt != nil {
		card.UpdatedAt = *job.CompletedAt
	}
	card.Jobs = JobSummary{Total: 1}
	if job.Status == "completed" {
		card.Jobs.Completed = 1
	}
	if isFailure(job.Conclusion) {
		card.Jobs.Failed = 1
	}
	return card
}

func (c *githubClient) listJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]Job, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=100", url.PathEscape(owner), url.PathEscape(repo), runID)
	if attempt > 0 {
		path = fmt.Sprintf("/repos/%s/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", url.PathEscape(owner), url.PathEscape(repo), runID, attempt)
	}
	var raw jobsResponse
	if _, _, err := c.getJSON(ctx, path, "", &raw); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(raw.Jobs))
	for _, item := range raw.Jobs {
		job := Job{ID: item.ID, Name: item.Name, Status: item.Status, Conclusion: item.Conclusion, StartedAt: item.StartedAt, CompletedAt: item.CompletedAt, URL: item.HTMLURL, RunnerName: item.RunnerName, Labels: append([]string(nil), item.Labels...)}
		for _, s := range item.Steps {
			job.Steps = append(job.Steps, Step{Number: s.Number, Name: s.Name, Status: s.Status, Conclusion: s.Conclusion, StartedAt: s.StartedAt, CompletedAt: s.CompletedAt})
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func summarizeJobs(jobs []Job) JobSummary {
	var summary JobSummary
	for _, job := range jobs {
		summary.Total++
		if job.Status == "completed" {
			summary.Completed++
		}
		if isFailure(job.Conclusion) {
			summary.Failed++
		}
	}
	return summary
}

func isFailure(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "action_required", "startup_failure":
		return true
	default:
		return false
	}
}

type retryAfterError struct {
	status int
	until  time.Time
	msg    string
}

func (e *retryAfterError) Error() string { return fmt.Sprintf("GitHub API %d: %s", e.status, e.msg) }

func (c *githubClient) getJSON(ctx context.Context, path, etag string, target any) (newETag string, notModified bool, err error) {
	newETag, _, notModified, err = c.getJSONPageWithETag(ctx, path, etag, target)
	return newETag, notModified, err
}

func (c *githubClient) getJSONPage(ctx context.Context, path string, target any) (next string, err error) {
	_, next, _, err = c.getJSONPageWithETag(ctx, path, "", target)
	return next, err
}

func (c *githubClient) getJSONPageWithETag(ctx context.Context, path, etag string, target any) (newETag, next string, notModified bool, err error) {
	targetURL := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		targetURL = strings.TrimRight(c.baseURL, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "rtk-local-ci-dashboard")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	if c.onRate != nil {
		c.onRate(resp.Header)
	}
	if resp.StatusCode == http.StatusNotModified {
		return etag, "", true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		until := retryUntil(resp.StatusCode, resp.Header)
		return "", "", false, &retryAfterError{status: resp.StatusCode, until: until, msg: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(target); err != nil {
		return "", "", false, err
	}
	return resp.Header.Get("ETag"), nextLink(resp.Header.Get("Link")), false, nil
}

func nextLink(value string) string {
	for _, part := range strings.Split(value, ",") {
		pieces := strings.Split(strings.TrimSpace(part), ";")
		if len(pieces) < 2 || !strings.Contains(pieces[1], `rel="next"`) {
			continue
		}
		link := strings.TrimSpace(pieces[0])
		if strings.HasPrefix(link, "<") && strings.HasSuffix(link, ">") {
			return strings.TrimSuffix(strings.TrimPrefix(link, "<"), ">")
		}
	}
	return ""
}
func retryUntil(status int, header http.Header) time.Time {
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && seconds > 0 {
		return time.Now().Add(time.Duration(seconds) * time.Second)
	}
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return time.Time{}
	}
	if header.Get("X-RateLimit-Remaining") != "0" {
		return time.Time{}
	}
	if epoch, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && epoch > 0 {
		return time.Unix(epoch, 0)
	}
	return time.Time{}
}

func (p *poller) captureRate(header http.Header) {
	limit, _ := strconv.Atoi(header.Get("X-RateLimit-Limit"))
	remaining, _ := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
	reset, _ := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64)
	if limit == 0 && remaining == 0 && reset == 0 {
		return
	}
	p.mu.Lock()
	p.rate = RateLimit{Limit: limit, Remaining: remaining}
	if reset > 0 {
		p.rate.ResetAt = time.Unix(reset, 0)
	}
	p.mu.Unlock()
}

func sanitizeError(err error) string {
	message := err.Error()
	if len(message) > 240 {
		message = message[:240] + "…"
	}
	return message
}
