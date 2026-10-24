package main

import (
	"errors"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type serviceClientInventoryReport struct {
	Status                 string    `json:"status"`
	Environment            string    `json:"environment"`
	Domain                 string    `json:"domain"`
	IssuerID               string    `json:"issuer_id"`
	RootFingerprint        string    `json:"root_fingerprint"`
	CheckedAt              time.Time `json:"checked_at"`
	Issuances              *int      `json:"issuances"`
	Pending                *int      `json:"pending_issuances"`
	Invalid                *int      `json:"invalid_records"`
	Revoked                *int      `json:"revoked_certificates"`
	Unpublished            *int      `json:"unpublished_revocations"`
	MissingAcknowledgments *int      `json:"missing_acknowledgments"`
}

func validateServiceClientInventoryReport(report serviceClientInventoryReport, namespace, issuer, root string, now time.Time) error {
	if !livePKIIssuerIDPattern.MatchString(issuer) || issuer == "00000000-0000-0000-0000-000000000000" || !deploymentImageDigestPattern.MatchString("sha256:"+root) || report.Status != "service-client-registry-inventory-checked" || !secretEnvironmentPattern.MatchString(report.Environment) || namespace != "video-cloud-"+report.Environment+"-video-cloud" || report.Domain != "service" || report.IssuerID != issuer || report.RootFingerprint != root || report.CheckedAt.IsZero() {
		return errors.New("Service inventory identity or snapshot time cannot be verified")
	}
	if report.CheckedAt.After(now.Add(5 * time.Second)) {
		return &deploymentplan.Blocker{ID: "deployment.pki.clock", Resource: "Deployment/" + namespace + "/pki-controller", Cause: "Service registry report time exceeds the verification clock by more than the allowed five seconds", Next: "verify trusted time synchronization on the checking machine and the selected pki-controller node; correct the unsynchronized clock and repeat preflight with the existing issuer, Root and freshness limits"}
	}
	if report.CheckedAt.Before(now.Add(-5 * time.Minute)) {
		return &deploymentplan.Blocker{ID: "deployment.pki.registry.stale", Resource: "Deployment/" + namespace + "/pki-controller", Cause: "Service registry report is older than the allowed five-minute evidence window", Next: "obtain a fresh read-only registry inventory; verify trusted clocks and controller query latency, then repeat preflight with the existing issuer and Root"}
	}
	for _, count := range []*int{report.Issuances, report.Pending, report.Invalid, report.Revoked, report.Unpublished, report.MissingAcknowledgments} {
		if count == nil || *count < 0 {
			return errors.New("Service inventory counters are absent or invalid")
		}
	}
	if *report.Issuances == 0 || *report.Revoked > *report.Issuances || *report.Pending != 0 || *report.Invalid != 0 || *report.Unpublished != 0 || *report.MissingAcknowledgments != 0 {
		return errors.New("Service registry requires reconciliation")
	}
	return nil
}
