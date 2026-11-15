package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ciRolloutInvocationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

type ciRolloutJournalEvent struct {
	Status        string    `json:"status"`
	At            time.Time `json:"at"`
	Namespace     string    `json:"namespace,omitempty"`
	Workload      string    `json:"workload,omitempty"`
	Container     string    `json:"container,omitempty"`
	UID           string    `json:"uid,omitempty"`
	PreviousImage string    `json:"previous_image,omitempty"`
	DesiredImage  string    `json:"desired_image,omitempty"`
}
type ciRolloutJournal struct {
	store       secretStore
	relative    string
	Environment string                             `json:"environment"`
	Repository  string                             `json:"repository"`
	Branch      string                             `json:"branch"`
	Image       environmentReleaseImage            `json:"image"`
	Images      map[string]environmentReleaseImage `json:"images,omitempty"`
	Events      []ciRolloutJournalEvent            `json:"events"`
}

func newCIRolloutBatchJournal(store secretStore, repo, branch string, images map[string]environmentReleaseImage, invocation string) (*ciRolloutJournal, error) {
	keys := make([]string, 0, len(images))
	for key := range images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return nil, errors.New("CI rollout: selected batch images required")
	}
	journal, err := newCIRolloutJournal(store, repo, branch, images[keys[0]], invocation)
	if err != nil {
		return nil, err
	}
	journal.Images = make(map[string]environmentReleaseImage, len(images))
	for _, key := range keys {
		image := images[key]
		if key != environmentImageRepository(image.Reference) || image.Publication == nil || image.Publication.Repository != repo || image.Publication.Branch != branch || !immutableEnvironmentImage.MatchString(image.Reference) {
			return nil, errors.New("CI rollout: invalid batch image")
		}
		journal.Images[key] = image
	}
	return journal, nil
}

func newCIRolloutJournal(store secretStore, repo, branch string, image environmentReleaseImage, invocation string) (*ciRolloutJournal, error) {
	if store.Environment != "dev" || !filepath.IsAbs(store.Root) || !ciRepositoryName.MatchString(repo) || !validCIBranchName(branch) || !ciRolloutInvocationID.MatchString(invocation) || !immutableEnvironmentImage.MatchString(image.Reference) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || image.Publication == nil || image.Publication.Repository != repo || image.Publication.RunID <= 0 || image.Publication.Attempt <= 0 {
		return nil, errors.New("CI rollout: valid environment journal inputs required")
	}
	name := strings.ReplaceAll(repo, "/", "_") + "-" + invocation + ".json"
	return &ciRolloutJournal{store: store, relative: filepath.Join("deployment", "ci-rollout", "journals", name), Environment: store.Environment, Repository: repo, Branch: branch, Image: image}, nil
}
func (j *ciRolloutJournal) record(ctx context.Context, status string, target ciRolloutTarget, previous string) error {
	return j.recordBatch(ctx, status, target, previous, "")
}

func (j *ciRolloutJournal) recordBatch(ctx context.Context, status string, target ciRolloutTarget, previous, desired string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if status != "started" && status != "applying" && status != "completed" && status != "failed" && status != "recovering" && status != "recovered" {
		return errors.New("CI rollout: invalid journal status")
	}
	if status == "applying" && (target.Namespace == "" || target.Name == "" || target.Container == "" || target.UID == "" || !immutableEnvironmentImage.MatchString(previous)) {
		return errors.New("CI rollout: prior immutable image required in journal")
	}
	if desired != "" && (!immutableEnvironmentImage.MatchString(desired) || environmentImageRepository(desired) != environmentImageRepository(previous)) {
		return errors.New("CI rollout: invalid target image in journal")
	}
	if len(j.Events) == 0 && status != "started" && status != "recovering" && status != "recovered" || len(j.Events) > 0 && status == "started" && j.Events[len(j.Events)-1].Status != "recovered" {
		return errors.New("CI rollout: invalid journal event order")
	}
	event := ciRolloutJournalEvent{Status: status, At: time.Now().UTC(), Namespace: target.Namespace, Workload: target.Name, Container: target.Container, UID: target.UID, PreviousImage: previous, DesiredImage: desired}
	if _, err := ciRolloutPrivatePath(j.store, j.relative); err != nil {
		return err
	}
	j.Events = append(j.Events, event)
	raw, err := json.Marshal(j)
	if err != nil {
		j.Events = j.Events[:len(j.Events)-1]
		return err
	}
	if err = j.store.write(j.relative, raw, len(j.Events) > 1); err != nil {
		j.Events = j.Events[:len(j.Events)-1]
		return err
	}
	return nil
}
