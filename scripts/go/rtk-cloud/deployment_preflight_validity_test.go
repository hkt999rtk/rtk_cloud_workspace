package main

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPreflightEvidenceValidityIsIndependentOfPKIWindow(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, seconds := range []string{"300", "1200"} {
			t.Run(environment+"/"+seconds, func(t *testing.T) {
				c, qualification, at := completePreflightProofFixture(t, environment)
				c.Configuration.Values[environmentPreflightValidityKey] = seconds
				proof, err := saveEnvironmentPreflightProof(c, qualification, at)
				if err != nil {
					t.Fatal("separate evidence validity could not be published", err)
				}
				validity, _ := strconv.ParseInt(seconds, 10, 64)
				if !proof.ValidUntil.Equal(at.Add(time.Duration(validity)*time.Second)) || !proof.PKICheckedAt.Equal(c.PKICheckedAt) || !proof.PKIVerifiedUntil.Equal(c.PKIVerifiedUntil) || proof.PKIVerifiedUntil.Sub(proof.PKICheckedAt) != 600*time.Second {
					t.Fatal("evidence expiry changed the original PKI verification interval")
				}
				admittedAt := proof.ValidUntil.Add(-time.Nanosecond)
				if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", c.Plan.SHA256(), admittedAt); err != nil {
					t.Fatal("evidence inherited the original PKI verification cutoff", err)
				}
				if err := validateEnvironmentPreflightProof(*proof, c.Configuration, c.Plan, admittedAt); err != nil {
					t.Fatal("an admitted timestamp could not preserve strict proof validation", err)
				}
				if _, err := loadEnvironmentPreflightProof(c.Configuration, c.Store, "upgrade", c.Plan.SHA256(), proof.ValidUntil); err == nil {
					t.Fatal("a new execution attempt could reuse evidence at its expiry boundary")
				}
			})
		}
	}
}

func TestPreflightProofReportsDistinctTimeAndPKIFailures(t *testing.T) {
	c, qualification, at := completePreflightProofFixture(t, "dev")
	proof, err := saveEnvironmentPreflightProof(c, qualification, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		cause string
		edit  func(*environmentPreflightProof, *time.Time)
		times bool
	}{
		{"expired", "qualification evidence has expired", func(p *environmentPreflightProof, now *time.Time) { *now = p.ValidUntil }, true},
		{"future", "qualification is future-dated", func(p *environmentPreflightProof, now *time.Time) { *now = p.QualifiedAt.Add(-time.Nanosecond) }, true},
		{"missing-completion", "qualification is missing its completion time", func(p *environmentPreflightProof, _ *time.Time) { p.QualifiedAt = time.Time{} }, true},
		{"missing-expiry", "qualification is missing its evidence expiry time", func(p *environmentPreflightProof, _ *time.Time) { p.ValidUntil = time.Time{} }, true},
		{"invalid-lifetime", "qualification evidence lifetime does not match DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS=600", func(p *environmentPreflightProof, _ *time.Time) { p.ValidUntil = p.ValidUntil.Add(time.Second) }, true},
		{"missing-pki", "qualification is missing its original completed PKI validity proof", func(p *environmentPreflightProof, _ *time.Time) { p.PKIVerifiedUntil = time.Time{} }, true},
		{"late-pki", "original PKI verification is later than qualification completion", func(p *environmentPreflightProof, _ *time.Time) { p.PKICheckedAt = p.QualifiedAt.Add(time.Second) }, true},
		{"invalid-pki-lifetime", "original PKI validity proof does not match DEPLOYMENT_UPGRADE_WINDOW_SECONDS=600", func(p *environmentPreflightProof, _ *time.Time) {
			p.PKIVerifiedUntil = p.PKIVerifiedUntil.Add(time.Second)
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, checkedAt := *proof, at
			changed.QualifiedAt = changed.QualifiedAt.In(time.FixedZone("Taipei", 8*60*60))
			changed.ValidUntil = changed.ValidUntil.In(time.FixedZone("Taipei", 8*60*60))
			test.edit(&changed, &checkedAt)
			err := validateEnvironmentPreflightProof(changed, c.Configuration, c.Plan, checkedAt)
			var failure *deploymentplan.Blocker
			if !errors.As(err, &failure) || failure.ID != "deployment.preflight.proof" || !strings.HasPrefix(failure.Cause, test.cause) {
				t.Fatal("time failure used a combined or inaccurate reason", err)
			}
			if test.times {
				for key, value := range map[string]time.Time{"qualified_at": changed.QualifiedAt, "valid_until": changed.ValidUntil, "checked_at": checkedAt} {
					if !strings.Contains(failure.Cause, key+"="+value.UTC().Format(time.RFC3339Nano)) {
						t.Fatal("time failure omitted an exact UTC evidence timestamp", key, failure.Cause)
					}
				}
			}
			if strings.Contains(failure.Cause, "synthetic-only") || strings.Contains(failure.Cause, "+08:00") {
				t.Fatal("time diagnostics included private inputs or ambiguous timezone formatting", failure.Cause)
			}
		})
	}
}

func TestPreflightValidityRejectsMissingMalformedAndImplicitPKIWindow(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "03600", "+3600", "3600s", "1.5", " 3600", "3600\n", "9223372037", "9223372036854775808", "private-unapproved-value"} {
		t.Run(raw, func(t *testing.T) {
			seconds, err := environmentPreflightValidity(map[string]string{environmentPreflightValidityKey: raw, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "3600"})
			var failure *deploymentplan.Blocker
			if seconds != 0 || !errors.As(err, &failure) || failure.ID != "deployment.preflight.validity" || failure.Resource != environmentPreflightValidityKey || failure.Next == "" || strings.Contains(err.Error(), "private-unapproved-value") {
				t.Fatal("invalid evidence validity inherited PKI policy or exposed its raw value", err)
			}
		})
	}
	for _, raw := range []string{"1", "3600", "9223372036"} {
		if seconds, err := environmentPreflightValidity(map[string]string{environmentPreflightValidityKey: raw}); err != nil || seconds <= 0 {
			t.Fatal("explicit bounded evidence validity was rejected", raw, err)
		}
	}
	c, qualification, at := completePreflightProofFixture(t, "dev")
	delete(c.Configuration.Values, environmentPreflightValidityKey)
	if _, err := saveEnvironmentPreflightProof(c, qualification, at); err == nil {
		t.Fatal("publisher reused the PKI window when evidence validity was absent")
	}
	active, err := readEnvironmentPreflightActive(c.Store)
	if err != nil || active.Decision != "NO-GO" {
		t.Fatal("invalid evidence validity published active GO", err)
	}
}

func TestSharedPreflightValidityAppliesToAllEnvironments(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	shared, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "architecture.env"))
	if err != nil || shared[environmentPreflightValidityKey] != "3600" {
		t.Fatal("shared preflight evidence validity is missing", err)
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			seconds, err := environmentPreflightValidity(cfg.Values)
			if err != nil || seconds != 3600 {
				t.Fatal("selected environment did not inherit the explicit shared evidence validity", err)
			}
		})
	}
}
