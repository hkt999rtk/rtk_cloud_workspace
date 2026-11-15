package main

import (
	"context"
	"encoding/json"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const issuerLeaseEnrollmentProfile = "certissuer-server-enrolled-lease-labels"

type issuerLeaseEnrollmentRecovery struct {
	plan    *deploymentplan.Plan
	journal deploymentplan.Journal
}

func (r issuerLeaseEnrollmentRecovery) isCandidate() bool {
	return r.plan != nil && r.plan.Environment == "dev" && r.plan.Stack == "video-cloud-dev" &&
		r.plan.Operation == "scoped" && strings.HasPrefix(r.journal.Failed, "Lease/"+r.plan.Stack+"-video-cloud/pki-state-")
}

// This is evidence for a separate forward recovery. Verification never changes
// the original journal or treats a successful Job as accepted deployment.
func (r issuerLeaseEnrollmentRecovery) verify(ctx context.Context, read func(context.Context, string) (deploymentplan.Object, error)) error {
	bad := func(resource, cause string) error {
		return &deploymentplan.Blocker{ID: "deployment.create.recovery.issuer-lease", Resource: resource, Cause: cause, Next: "preserve the original journal and reconcile the exact recorded owners before continuation"}
	}
	if !r.isCandidate() || r.plan.Version != deploymentplan.Version || r.journal.PlanSHA256 != r.plan.SHA256() ||
		r.journal.Accepted || r.journal.AcceptanceStarted || r.journal.InFlight != "" ||
		len(r.plan.Resources) != 7 || len(r.journal.Completed) != 7 || len(r.journal.Owners) != 7 {
		return bad("", "original stage must have seven completed owner receipts and only a post-write Lease verification failure")
	}
	completed := map[string]bool{}
	for _, id := range r.journal.Completed {
		if completed[id] {
			return bad(id, "duplicate original completion evidence")
		}
		completed[id] = true
	}
	issuerNamespace := r.plan.Stack + "-video-cloud"
	leaseName := strings.TrimPrefix(r.journal.Failed, "Lease/"+issuerNamespace+"/")
	leaseChecked, jobChecked := false, false
	for _, resource := range r.plan.Resources {
		uid := r.journal.Owners[resource.ID]
		if !completed[resource.ID] || resource.ID != deploymentplan.ID(resource.Desired) || resource.Phase != "bootstrap" ||
			resource.Action != "retain" && resource.Action != "create" || !environmentBootstrapOwnerUUID.MatchString(uid) ||
			resource.UID != "" && resource.UID != uid {
			return bad(resource.ID, "invalid original stage owner receipt")
		}
		delete(completed, resource.ID)
		actual, err := read(ctx, resource.ID)
		if err != nil {
			return err
		}
		meta := deploymentplan.Map(actual["metadata"])
		if actual == nil || meta["uid"] != uid || meta["deletionTimestamp"] != nil {
			return bad(resource.ID, "recorded original resource is missing, replaced or deleting")
		}
		switch deploymentplan.String(resource.Desired["kind"]) {
		case "Lease":
			if resource.ID != r.journal.Failed || resource.Action != "create" || len(leaseName) != len("pki-state-")+32 ||
				resource.Desired["apiVersion"] != "coordination.k8s.io/v1" ||
				len(deploymentplan.Map(resource.Desired["spec"])) != 0 || deploymentplan.String(meta["resourceVersion"]) == "" ||
				deploymentplan.String(deploymentplan.Map(actual["spec"])["holderIdentity"]) != "" {
				return bad(resource.ID, "original custody Lease does not match the released owner contract")
			}
			labels := deploymentplan.Map(deploymentplan.Map(resource.Desired["metadata"])["labels"])
			observed := deploymentplan.Map(meta["labels"])
			if len(labels) != 2 || labels["app.kubernetes.io/managed-by"] != "rtk-cloud" || labels["rtk.realtek.com/stack"] != r.plan.Stack {
				return bad(resource.ID, "original ownership labels are unavailable")
			}
			check := deploymentplan.Clone(actual)
			checkLabels := deploymentplan.Object{}
			for key, value := range labels {
				if live, exists := observed[key]; exists && live != value {
					return bad(resource.ID, "live Lease contains conflicting ownership labels")
				}
				checkLabels[key] = value
			}
			deploymentplan.Map(check["metadata"])["labels"] = checkLabels
			if !deploymentplan.Matches(resource.Desired, check) {
				return bad(resource.ID, "Lease differs beyond the original ownership labels")
			}
			leaseChecked = true
		case "Job":
			if jobChecked || resource.Action != "create" || !strings.HasPrefix(resource.ID, "Job/"+issuerNamespace+"/pki-enroll-certissuer-server-") {
				return bad(resource.ID, "original enrollment Job is ambiguous")
			}
			for _, raw := range certIssuerObjectList(deploymentplan.Map(actual["status"])["conditions"]) {
				condition := deploymentplan.Map(raw)
				if condition["type"] == "Failed" && condition["status"] == "True" {
					return bad(resource.ID, "original enrollment Job failed")
				}
				jobChecked = jobChecked || condition["type"] == "Complete" && condition["status"] == "True"
			}
		}
		if resource.ID != r.journal.Failed && !deploymentplan.Matches(resource.Desired, actual) {
			return bad(resource.ID, "recorded original resource differs from the qualified body")
		}
		if resource.Action == "retain" {
			if _, err := resource.Body(actual); err != nil {
				return err
			}
		}
	}
	if !leaseChecked || !jobChecked {
		return bad(r.journal.Failed, "original released Lease and completed CertIssuer enrollment Job must both be verified")
	}
	return nil
}

func issuerLeaseLabelPatch(actual, labels deploymentplan.Object) []byte {
	meta := deploymentplan.Map(actual["metadata"])
	next := deploymentplan.Object{}
	for k, v := range deploymentplan.Map(meta["labels"]) {
		next[k] = v
	}
	for k, v := range labels {
		next[k] = v
	}
	raw, _ := json.Marshal([]deploymentplan.Object{
		{"op": "test", "path": "/metadata/uid", "value": meta["uid"]},
		{"op": "test", "path": "/metadata/resourceVersion", "value": meta["resourceVersion"]},
		{"op": "add", "path": "/metadata/labels", "value": next},
	})
	return raw
}

// Reuse the same live read for evidence and the compare-and-set input.
func (r issuerLeaseEnrollmentRecovery) checkLabelPatch(ctx context.Context, backend environmentKubernetesBackend) error {
	_, err := r.prepareLabelPatch(ctx, backend)
	return err
}

type issuerLeasePatchPlan struct {
	Resource  string          `json:"resource"`
	BeforeSHA string          `json:"before_sha256"`
	AfterSHA  string          `json:"after_sha256"`
	Patch     json.RawMessage `json:"patch"`
}

func (r issuerLeaseEnrollmentRecovery) prepareLabelPatch(ctx context.Context, backend environmentKubernetesBackend) (issuerLeasePatchPlan, error) {
	var plan issuerLeasePatchPlan
	var actual deploymentplan.Object
	if err := r.verify(ctx, func(ctx context.Context, id string) (deploymentplan.Object, error) {
		object, err := backend.Read(ctx, id)
		if id == r.journal.Failed {
			actual = object
		}
		return object, err
	}); err != nil {
		return plan, err
	}
	for _, resource := range r.plan.Resources {
		if resource.ID != r.journal.Failed {
			continue
		}
		meta := deploymentplan.Map(actual["metadata"])
		wantLabels := deploymentplan.Map(deploymentplan.Map(resource.Desired["metadata"])["labels"])
		plan.Resource = resource.ID
		plan.BeforeSHA = deploymentplan.Digest(actual)
		plan.Patch = issuerLeaseLabelPatch(actual, wantLabels)
		observed, err := plan.call(ctx, backend, true)
		if err != nil {
			return plan, err
		}
		want := deploymentplan.Projection(actual)
		merged := deploymentplan.Object{}
		for k, v := range deploymentplan.Map(meta["labels"]) {
			merged[k] = v
		}
		for k, v := range wantLabels {
			merged[k] = v
		}
		deploymentplan.Map(want["metadata"])["labels"] = merged
		plan.AfterSHA = deploymentplan.Digest(want)
		if deploymentplan.Digest(deploymentplan.Projection(observed)) != plan.AfterSHA {
			return plan, createProviderRecoveryBlock("native dry-run changed fields outside the original Lease ownership labels")
		}
		return plan, r.admitAfterLabelRepair(ctx, backend, backend.Store.Root, actual, observed)
	}
	return plan, createProviderRecoveryBlock("original Lease label correction has no recorded target")
}

func (p issuerLeasePatchPlan) call(ctx context.Context, backend environmentKubernetesBackend, dry bool) (deploymentplan.Object, error) {
	target, err := environmentResourceArguments(p.Resource)
	if err != nil {
		return nil, err
	}
	args := append([]string{"patch"}, target...)
	args = append(args, "--type=json", "--patch", string(p.Patch), "-o", "json")
	if dry {
		args = append(args, "--dry-run=server")
	}
	raw, err := backend.command(ctx, nil, args...)
	var observed deploymentplan.Object
	if err != nil || json.Unmarshal(raw, &observed) != nil || deploymentplan.ID(observed) != p.Resource {
		return nil, createProviderRecoveryBlock("native API rejected the exact original Lease label correction or returned an unexpected resource")
	}
	return observed, nil
}

// Admit the exact original journal against the server-validated label change.
// The synthetic observation must not mask a concurrent live Lease update.
func (r issuerLeaseEnrollmentRecovery) admitAfterLabelRepair(ctx context.Context, backend deploymentplan.Backend, root string, before, after deploymentplan.Object) error {
	return r.plan.AdmitResumable(ctx, issuerLeaseDryRunBackend{
		Backend: backend, id: r.journal.Failed, before: deploymentplan.Digest(before), after: after,
	}, root)
}

type issuerLeaseDryRunBackend struct {
	deploymentplan.Backend
	id, before string
	after      deploymentplan.Object
}

func (b issuerLeaseDryRunBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	actual, err := b.Backend.Read(ctx, id)
	if err != nil || id != b.id {
		return actual, err
	}
	if actual == nil || deploymentplan.Digest(actual) != b.before {
		return nil, createProviderRecoveryBlock("original Lease changed since the exact label correction dry-run")
	}
	return b.after, nil
}
