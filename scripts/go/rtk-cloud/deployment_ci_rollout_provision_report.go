package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

type ciRolloutProvisionReport struct {
	store       secretStore
	relative    string
	Environment string    `json:"environment"`
	Started     time.Time `json:"started"`
	Status      string    `json:"status"`
	Stage       string    `json:"stage"`
	Repository  string    `json:"repository,omitempty"`
	Error       string    `json:"error,omitempty"`
}

func newCIRolloutProvisionReport(store secretStore) (*ciRolloutProvisionReport, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	report := &ciRolloutProvisionReport{store: store, relative: filepath.Join("deployment", "ci-rollout", "provision", "attempt-"+hex.EncodeToString(nonce)+".json"), Environment: store.Environment, Started: time.Now().UTC(), Status: "running", Stage: "start"}
	if err := report.save(false); err != nil {
		return nil, err
	}
	return report, nil
}
func (r *ciRolloutProvisionReport) save(replace bool) error {
	if r == nil || r.Environment != "dev" || r.store.Environment != r.Environment {
		return errors.New("CI rollout: invalid provisioning report")
	}
	if _, err := ciRolloutPrivatePath(r.store, r.relative); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return r.store.write(r.relative, raw, replace)
}
func (r *ciRolloutProvisionReport) step(stage, repo string) error {
	r.Stage, r.Repository = stage, repo
	return r.save(true)
}
