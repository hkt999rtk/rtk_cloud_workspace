package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func maintenanceOnlineArguments(environment string, issuer environmentCreateRegisteredIssuer, prior createRootCRLRecord, now time.Time) ([]string, bool, error) {
	if issuer.Document["signer_provider"] != "openbao" {
		return nil, false, nil
	}
	if issuer.Environment != environment || (environment != "dev" && environment != "staging" && environment != "prod") {
		return nil, false, errors.New("online CRL environment differs")
	}
	if _, err := validateMaintenanceCRL(environment, issuer, prior, false, now); err != nil {
		return nil, false, err
	}
	// Match the native worker's one-hour refresh lead; healthy workers continue
	// normal renewal without an extra provider call on every scheduled run.
	if prior.NextUpdate.After(now.Add(time.Hour)) {
		return nil, false, nil
	}
	pin := deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"])
	if !environmentBootstrapOwnerUUID.MatchString(issuer.ID) || !environmentPreflightDigest.MatchString(pin) || issuer.Reference == "" {
		return nil, false, errors.New("original online CRL issuer binding is missing")
	}
	return []string{"recovery-renew-online-crl", environment, issuer.ID, pin, issuer.Reference, prior.Digest}, true, nil
}

func (r *maintenanceRuntime) recoverOnline(ctx context.Context, inventory crlMaintenanceInventory, records map[string]createRootCRLRecord) (resultErr error) {
	var helper *crlMaintenanceHelper
	defer func() {
		if helper != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := helper.Close(cleanup); resultErr == nil && err != nil {
				resultErr = err
			}
		}
	}()
	for _, item := range inventory.Issuers {
		issuer := item.Issuer
		if issuer.Document["signer_provider"] != "openbao" {
			continue
		}
		// Native workers may have advanced since the initial inventory. Reuse that
		// successor instead of issuing another rotation from a stale snapshot.
		current, err := r.snapshot(ctx, issuer)
		if err != nil {
			return err
		}
		records[issuer.ID] = current.Prior
		args, due, err := maintenanceOnlineArguments(r.cfg.Environment, issuer, current.Prior, time.Now().UTC())
		if err != nil {
			return err
		}
		if !due {
			continue
		}
		if r.lease == nil {
			return errors.New("online recovery requires environment ownership")
		}
		if err := r.lease.Check(ctx); err != nil {
			return err
		}
		if _, err := r.nativeTrustBinary(ctx); err != nil {
			return err
		}
		if helper == nil {
			if r.image == "" {
				if r.ensureImage == nil {
					return errors.New("online recovery CI helper resolver missing")
				}
				r.image, err = r.ensureImage(ctx)
				if err != nil {
					return err
				}
			}
			helper, err = newCRLMaintenanceOnlineHelper(ctx, r.cfg, r.kubeconfig, r.image)
			if err != nil {
				return err
			}
		}
		path := crlMaintenanceBase + "/online/" + issuer.ID + "/" + current.Prior.Digest + ".json"
		evidence := map[string]any{"environment": r.cfg.Environment, "issuer": issuer.Document, "prior": current.Prior, "started_at": time.Now().UTC()}
		if err := maintenanceWrite(r.store, path, evidence); err != nil {
			return err
		}
		if _, err := helper.Run(ctx, args, nil); err != nil {
			return fmt.Errorf("online CRL %s qualification: %w", issuer.ID, err)
		}
		if err := r.lease.Check(ctx); err != nil {
			return err
		}
		raw, err := helper.Run(ctx, append(args, "--confirm", r.cfg.Environment), nil)
		if err != nil {
			return fmt.Errorf("online CRL %s recovery: %w", issuer.ID, err)
		}
		var result struct {
			Status      string              `json:"status"`
			Environment string              `json:"environment"`
			CRL         createRootCRLRecord `json:"crl"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Status != "imported" || result.Environment != r.cfg.Environment || result.CRL.IssuerID != issuer.ID {
			return errors.New("online CRL publication readback differs")
		}
		if _, err := validateMaintenanceCRL(r.cfg.Environment, issuer, result.CRL, true, time.Now()); err != nil {
			return err
		}
		// The native guard preserves revocations. Independently confirm the registry
		// published the exact signed result before touching any consumer state.
		actual, err := r.snapshot(ctx, issuer)
		if err != nil {
			return err
		}
		if actual.Prior.Digest != result.CRL.Digest {
			return errors.New("online CRL registry differs from signed successor")
		}
		records[issuer.ID] = result.CRL
		evidence["result"] = result
		evidence["published_at"] = time.Now().UTC()
		if err := maintenanceWrite(r.store, path, evidence); err != nil {
			return err
		}
	}
	// This also repairs a prior interrupted installation whose registry record
	// is already fresh. Only existing original consumer paths are touched.
	for _, c := range inventory.Consumers {
		var issuer environmentCreateRegisteredIssuer
		for _, item := range inventory.Issuers {
			if item.Issuer.ID == c.IssuerID {
				issuer = item.Issuer
				break
			}
		}
		if issuer.Document["signer_provider"] != "openbao" {
			continue
		}
		if err := r.install(ctx, c, issuer, records[c.IssuerID]); err != nil {
			return err
		}
	}
	return nil
}
