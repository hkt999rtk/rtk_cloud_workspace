package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

const crlMaintenanceBase = "deployment/crl-maintenance"
const crlMaintenanceLead = 48 * time.Hour

type crlMaintenanceIssuer struct {
	Issuer   environmentCreateRegisteredIssuer
	Snapshot createRootCRLNativeSnapshot
}
type crlMaintenanceConsumer struct{ Namespace, Pod, PodUID, Container, StatePath, IssuerID, ConsumerID string }
type crlMaintenanceInventory struct {
	Issuers   []crlMaintenanceIssuer
	Consumers []crlMaintenanceConsumer
	Public    []maintenancePublicCRL
	Required  map[string][]string
	Restart   bool
}
type crlMaintenanceOptions struct {
	Environment, ClusterSHA string
	Execute, Recover        bool
	Now                     func() time.Time
}
type crlMaintenanceDependencies struct {
	Prepare  func(context.Context, crlMaintenanceIssuer) (crlMaintenanceIssuer, error)
	Observe  func(context.Context) (crlMaintenanceInventory, error)
	Sign     func(context.Context, crlMaintenanceIssuer, createRootCRLRequest, string) (createRootCRLRecord, error)
	Import   func(context.Context, crlMaintenanceIssuer, createRootCRLRequest, createRootCRLRecord) error
	Converge func(context.Context, crlMaintenanceInventory, map[string]createRootCRLRecord) error
}
type crlMaintenanceStatus struct {
	Environment string    `json:"environment"`
	CheckedAt   time.Time `json:"checked_at"`
	NextExpiry  time.Time `json:"next_expiry"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	Due         bool      `json:"due"`
	Completed   bool      `json:"completed"`
	Issuers     int       `json:"issuers"`
	Consumers   int       `json:"consumers"`
	Failure     string    `json:"failure,omitempty"`
}
type crlMaintenanceJournal struct {
	Schema      string               `json:"schema"`
	Environment string               `json:"environment"`
	ClusterSHA  string               `json:"cluster_sha256"`
	Original    crlMaintenanceIssuer `json:"original"`
	Request     createRootCRLRequest `json:"request"`
	Signed      createRootCRLRecord  `json:"signed"`
	Completed   bool                 `json:"completed"`
}

func maintenanceRead(store secretStore, relative string, value any) error {
	path, err := maintenancePath(store, relative)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
func maintenanceWrite(store secretStore, relative string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = maintenancePath(store, relative)
	if err != nil {
		return err
	}
	return store.write(relative, raw, true)
}

func maintenancePath(store secretStore, relative string) (string, error) {
	path, err := store.safePath(relative)
	if err != nil {
		return "", err
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
				return "", errors.New("CRL evidence requires private real directories")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		if p == store.ConfigRoot {
			break
		}
		if p == filepath.Dir(p) {
			return "", errors.New("CRL evidence escaped environment")
		}
	}
	if info, e := os.Lstat(path); e == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", errors.New("CRL evidence requires private regular files")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	return path, nil
}

// The engine signs once, reconciles ambiguous publication against native state,
// and retries installation until actual consumer evidence is complete.
func executeCRLMaintenance(ctx context.Context, store secretStore, o crlMaintenanceOptions, d crlMaintenanceDependencies) (status crlMaintenanceStatus, resultErr error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	now := o.Now().UTC().Truncate(time.Second)
	status = crlMaintenanceStatus{Environment: o.Environment, CheckedAt: now}
	var last crlMaintenanceStatus
	if maintenanceRead(store, crlMaintenanceBase+"/status.json", &last) == nil && last.Environment == o.Environment {
		status.LastSuccess = last.LastSuccess
	}
	if store.Environment != o.Environment || (o.Environment != "dev" && o.Environment != "staging" && o.Environment != "prod") {
		return status, errors.New("explicit matching dev, staging or prod environment required")
	}
	defer func() {
		if !o.Execute {
			return
		}
		if resultErr != nil {
			status.Failure = resultErr.Error()
		}
		if err := maintenanceWrite(store, crlMaintenanceBase+"/status.json", status); resultErr == nil && err != nil {
			resultErr = err
		}
	}()
	inventory, err := d.Observe(ctx)
	if err != nil {
		return status, err
	}
	status.Issuers = len(inventory.Issuers)
	status.Consumers = len(inventory.Consumers)
	if len(inventory.Issuers) == 0 {
		return status, errors.New("selected registry has no active issuers")
	}
	records := map[string]createRootCRLRecord{}
	journals := map[string]crlMaintenanceJournal{}
	for _, item := range inventory.Issuers {
		if err := ctx.Err(); err != nil {
			return status, err
		}
		issuer := item.Issuer
		prior := item.Snapshot.Prior
		if _, exists := records[issuer.ID]; exists {
			return status, errors.New("duplicate registry issuer")
		}
		if _, err := validateMaintenanceCRL(o.Environment, issuer, prior, false, now); err != nil {
			return status, fmt.Errorf("issuer %s: %w", issuer.ID, err)
		}
		records[issuer.ID] = prior
		if status.NextExpiry.IsZero() || prior.NextUpdate.Before(status.NextExpiry) {
			status.NextExpiry = prior.NextUpdate
		}
		offline := issuer.Kind == "root" && issuer.Document["signer_provider"] == "offline"
		due := !prior.NextUpdate.After(now.Add(crlMaintenanceLead))
		if offline && due {
			status.Due = true
		}
		if !o.Execute {
			continue
		}
		if !prior.NextUpdate.After(now) && !o.Recover {
			return status, errors.New("expired CRL requires explicit --recover")
		}
		if !offline {
			continue
		}
		relative := crlMaintenanceBase + "/issuers/" + issuer.ID + "/current.json"
		var j crlMaintenanceJournal
		err := maintenanceRead(store, relative, &j)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return status, fmt.Errorf("original CRL renewal journal cannot be read: %w", err)
		}
		resume := err == nil && !j.Completed
		if !due && !resume {
			continue
		}
		if resume {
			if j.Schema != "rtk-crl-maintenance/v1" || j.Environment != o.Environment || j.ClusterSHA != o.ClusterSHA || j.Original.Issuer.ID != issuer.ID || j.Original.Issuer.RootFingerprint != issuer.RootFingerprint {
				return status, errors.New("CRL renewal journal belongs to another authority or cluster")
			}
			if prior.Digest != j.Original.Snapshot.Prior.Digest && prior.Digest != j.Signed.Digest {
				return status, errors.New("registry advanced beyond interrupted CRL renewal; preserve evidence")
			}
		} else {
			if d.Prepare != nil {
				item, err = d.Prepare(ctx, item)
				if err != nil {
					return status, err
				}
			}
			request, err := buildMaintenanceCRLRequest(o.Environment, item, now)
			if err != nil {
				return status, err
			}
			j = crlMaintenanceJournal{Schema: "rtk-crl-maintenance/v1", Environment: o.Environment, ClusterSHA: o.ClusterSHA, Original: item, Request: request}
			if err := maintenanceWrite(store, relative, j); err != nil {
				return status, err
			}
		}
		candidate := crlMaintenanceBase + "/issuers/" + issuer.ID + "/" + deploymentplan.Digest(j.Request)
		if !j.Original.Snapshot.Prior.NextUpdate.After(now) {
			inventory.Restart = true
		}
		if j.Signed.Digest == "" {
			if d.Sign == nil {
				return status, errors.New("native signing capability unavailable")
			}
			j.Signed, err = d.Sign(ctx, j.Original, j.Request, candidate)
			if err != nil {
				return status, err
			}
			if err := validateMaintenanceSuccessor(o.Environment, j.Original, j.Request, j.Signed, now); err != nil {
				return status, err
			}
			if err := maintenanceWrite(store, relative, j); err != nil {
				return status, err
			}
		}
		if err := validateMaintenanceSuccessor(o.Environment, j.Original, j.Request, j.Signed, now); err != nil {
			return status, err
		}
		if prior.Digest != j.Signed.Digest {
			// A fresh native importer rechecks complete live revocations and predecessor
			// transactionally. A changed inventory cannot be silently dropped.
			if d.Import == nil {
				return status, errors.New("native import capability unavailable")
			}
			if err := d.Import(ctx, j.Original, j.Request, j.Signed); err != nil {
				return status, err
			}
		}
		records[issuer.ID] = j.Signed
		journals[relative] = j
	}
	if !o.Execute {
		return status, nil
	}
	if d.Converge == nil {
		return status, errors.New("native consumer convergence unavailable")
	}
	if err := d.Converge(ctx, inventory, records); err != nil {
		return status, err
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	for relative, j := range journals {
		j.Completed = true
		if err := maintenanceWrite(store, relative, j); err != nil {
			return status, err
		}
	}
	status.NextExpiry = time.Time{}
	for _, r := range records {
		if status.NextExpiry.IsZero() || r.NextUpdate.Before(status.NextExpiry) {
			status.NextExpiry = r.NextUpdate
		}
	}
	status.Due = false
	status.Completed = true
	status.LastSuccess = o.Now().UTC()
	return status, nil
}
