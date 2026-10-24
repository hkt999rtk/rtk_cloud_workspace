package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Only complete candidate qualification supplies preflightPlan. Health-only
// and final acceptance retain readiness checks; no operator bypass flag exists.
func (r *deploymentCheckRuntime) checkPKIWorkloadPhase(namespace string, d liveDeployment) error {
	id := "Deployment/" + namespace + "/" + d.Metadata.Name
	if r == nil || r.preflightPlan == nil {
		if !livePKIWorkloadReady(d) {
			return fmt.Errorf("%s is not ready for post-deploy PKI health acceptance", id)
		}
		return nil
	}
	p := r.preflightPlan
	if p.Environment != r.environment || p.Stack != "video-cloud-"+r.environment || p.Operation != "upgrade" && !p.HasCreateHandoff() {
		return fmt.Errorf("%s has no matching preflight environment and operation", id)
	}
	for _, resource := range p.Resources {
		if resource.ID != id {
			continue
		}
		if (resource.Action != "update" && resource.Action != "retain") || resource.UID == "" || resource.UID != d.Metadata.UID {
			return fmt.Errorf("%s original PKI owner does not match the reviewed plan", id)
		}
		if !livePKIWorkloadReady(d) {
			if r.pendingHealth == nil {
				r.pendingHealth = map[string]bool{}
			}
			r.pendingHealth[id] = true
		}
		return nil
	}
	return fmt.Errorf("%s PKI consumer is absent from the reviewed plan", id)
}

func (r *deploymentCheckRuntime) pendingRolloutHealth() []string {
	var ids []string
	for id := range r.pendingHealth {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// A complete native inventory still validates lineage, signed current CRLs,
// issuance records and revocations when its only failure is missing consumer
// acknowledgments. Those receipts require the applications to execute. Defer
// them only for original consumers included in this exact upgrade plan.
func (r *deploymentCheckRuntime) deferServiceRegistryAcknowledgments(report serviceClientInventoryReport, kubeconfig, namespace, issuer, root, consumers string, deployments liveDeploymentList) (bool, error) {
	if r == nil || r.preflightPlan == nil || report.Status != "service-client-registry-inventory-blocked" || report.MissingAcknowledgments == nil || *report.MissingAcknowledgments <= 0 {
		return false, nil
	}
	zero := 0
	prerequisites := report
	prerequisites.Status = "service-client-registry-inventory-checked"
	prerequisites.MissingAcknowledgments = &zero
	if err := validateServiceClientInventoryReport(prerequisites, namespace, issuer, root, time.Now().UTC()); err != nil {
		return false, err
	}
	owners := map[string]string{
		"account-manager":         "Deployment/" + r.preflightPlan.Stack + "-account-manager/account-manager",
		"certissuer":              "Deployment/" + namespace + "/certissuer",
		"factory-enroll":          "Deployment/" + namespace + "/factoryenroll",
		"pki-controller":          "Deployment/" + namespace + "/pki-controller",
		"video-cloud-api":         "Deployment/" + namespace + "/video-cloud-api-pki",
		"video-cloud-logingester": "Deployment/" + namespace + "/video-cloud-logingester",
	}
	ids, seen := strings.Split(consumers, ","), map[string]bool{}
	if *report.MissingAcknowledgments > 2*len(ids) || r.preflightPlan.Environment != r.environment || r.preflightPlan.Stack != "video-cloud-"+r.environment || r.preflightPlan.Operation != "upgrade" && !r.preflightPlan.HasCreateHandoff() {
		return false, fmt.Errorf("Service registry acknowledgment evidence is not bound to the reviewed upgrade")
	}
	var pending []string
	for _, consumer := range ids {
		id := owners[consumer]
		if id == "" || seen[consumer] {
			return false, fmt.Errorf("Service CRL consumer has no unique supported original owner in the reviewed upgrade")
		}
		seen[consumer] = true
		var observed liveDeploymentList
		if consumer == "account-manager" {
			raw, err := r.kubectl(false, "--kubeconfig", kubeconfig, "-n", r.preflightPlan.Stack+"-account-manager", "get", "deployments", "-o", "json")
			if err != nil || json.Unmarshal(raw, &observed) != nil {
				return false, secretCheckFailure(err, "original Account Manager owner cannot be verified for deferred CRL acknowledgment")
			}
		} else {
			observed = deployments
		}
		matched := false
		for _, resource := range r.preflightPlan.Resources {
			if resource.ID != id || resource.UID == "" || resource.Action != "update" && resource.Action != "retain" {
				continue
			}
			for _, d := range observed.Items {
				if strings.HasSuffix(id, "/"+d.Metadata.Name) && d.Metadata.UID == resource.UID {
					matched = true
				}
			}
		}
		if !matched {
			return false, fmt.Errorf("Service CRL consumer %s original owner is absent or changed in the reviewed upgrade", consumer)
		}
		pending = append(pending, id)
	}
	if r.pendingHealth == nil {
		r.pendingHealth = map[string]bool{}
	}
	for _, id := range pending {
		r.pendingHealth[id] = true
	}
	return true, nil
}
