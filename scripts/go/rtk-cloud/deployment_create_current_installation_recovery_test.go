package main

import (
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCurrentInstallationRecoveryRejectsUnknownWrites(t *testing.T) {
	stack := "video-cloud-dev"
	ns := stack + "-video-cloud"
	var defs []deploymentplan.Definition
	for _, owner := range environmentCreateApplicationOwners() {
		defs = append(defs,
			deploymentplan.Definition{Phase: "storage", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": owner.Claim, "namespace": ns}, "spec": deploymentplan.Object{}}},
			deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "pki-enroll-" + owner.Name + "-client-original", "namespace": ns}, "spec": deploymentplan.Object{}}})
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("native")}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Owners: map[string]string{}, Failed: "Job/" + ns + "/pki-enroll-video-cloud-api-client-original"}
	for _, r := range plan.Resources {
		if r.Desired["kind"] == "PersistentVolumeClaim" || currentInstallationContains([]string{"Job/" + ns + "/pki-enroll-factoryenroll-client-original", "Job/" + ns + "/pki-enroll-mqtt-pki-client-original", "Job/" + ns + "/pki-enroll-video-cloud-api-app-pki-client-original"}, r.ID) {
			j.Completed = append(j.Completed, r.ID)
			j.Owners[r.ID] = "original-owner"
		}
	}
	j.Owners[j.Failed] = "original-failed-job"
	if !knownCurrentInstallationFailure(plan, j, stack) {
		t.Fatal("exact native checkpoint rejected")
	}
	for _, mode := range []string{"unknown-owner", "different-failure", "in-flight", "accepted", "missing-completion"} {
		t.Run(mode, func(t *testing.T) {
			copy := j
			copy.Owners = map[string]string{}
			for k, v := range j.Owners {
				copy.Owners[k] = v
			}
			switch mode {
			case "unknown-owner":
				copy.Owners["Job/"+ns+"/unqualified"] = "foreign"
			case "different-failure":
				copy.Failed = "Job/" + ns + "/pki-enroll-mqtt-pki-client-original"
			case "in-flight":
				copy.InFlight = copy.Failed
			case "accepted":
				copy.Accepted = true
			case "missing-completion":
				copy.Completed = copy.Completed[1:]
			}
			if knownCurrentInstallationFailure(plan, copy, stack) {
				t.Fatal("unknown progress admitted")
			}
		})
	}
	key := "kubernetes/application-current-installation-renewal-" + deploymentplan.Digest("qualified")
	journal := environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "kubernetes/openbao-root-trust", State: "pending", IntentSHA256: deploymentplan.Digest("original")}, {Key: key, State: "pending", IntentSHA256: deploymentplan.Digest("partial")}}}
	if profile, _ := createRecoveryCheckpointProfile(journal); profile != "current-application-identity-lock" {
		t.Fatal("create did not select original owner recovery")
	}
	journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: "unknown", State: "pending", IntentSHA256: deploymentplan.Digest("other"), StartedAt: time.Now()})
	if profile, _ := createRecoveryCheckpointProfile(journal); profile != "" {
		t.Fatal("unknown concurrent checkpoint admitted")
	}
}

func TestCreateSelectsInterruptedOriginalOwnerPause(t *testing.T) {
	journal := environmentCreateJournal{Entries: []environmentCreateJournalEntry{
		{Key: "kubernetes/openbao-root-trust", State: "pending", IntentSHA256: deploymentplan.Digest("root")},
		{Key: "kubernetes/application-current-installation-renewal-" + deploymentplan.Digest("seed"), State: "pending", IntentSHA256: deploymentplan.Digest("app")},
		{Key: "kubernetes/current-owner-quiesce-" + deploymentplan.Digest("pause"), State: "pending", IntentSHA256: deploymentplan.Digest("paused")},
	}}
	if p, _ := createRecoveryCheckpointProfile(journal); p != "current-owner-pause-acceptance" {
		t.Fatal("original pause checkpoint not selected")
	}
	journal.Entries[2].Key = "kubernetes/unknown-" + deploymentplan.Digest("pause")
	if p, _ := createRecoveryCheckpointProfile(journal); p != "" {
		t.Fatal("unknown partial writes admitted")
	}
}

func TestCurrentIdentityOwnerSelectorUsesOriginalLabels(t *testing.T) {
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		for _, key := range []string{"app.kubernetes.io/name", "app"} {
			object := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{key: name, "environment": "dev"}}}}
			selector, err := currentIdentityOwnerSelector("video-cloud-dev", object)
			if err != nil || selector != key+"="+name+",environment=dev" {
				t.Fatal("original selector not preserved", selector, err)
			}
			deploymentplan.Map(object["metadata"])["namespace"] = "video-cloud-staging-video-cloud"
			if _, err := currentIdentityOwnerSelector("video-cloud-dev", object); err == nil {
				t.Fatal("foreign namespace accepted")
			}
		}
	}
	journal := environmentCreateJournal{Entries: []environmentCreateJournalEntry{
		{Key: "kubernetes/openbao-root-trust", State: "pending", IntentSHA256: deploymentplan.Digest("root")},
		{Key: "kubernetes/application-current-installation-renewal-" + deploymentplan.Digest("seed"), State: "pending", IntentSHA256: deploymentplan.Digest("app")},
		{Key: "kubernetes/current-owner-quiesce-" + deploymentplan.Digest("first"), State: "pending", IntentSHA256: deploymentplan.Digest("pause1")},
		{Key: "kubernetes/current-owner-quiesce-" + deploymentplan.Digest("second"), State: "pending", IntentSHA256: deploymentplan.Digest("pause2")},
	}}
	if p, _ := createRecoveryCheckpointProfile(journal); p != "current-owner-pause-acceptance" {
		t.Fatal("recorded pause chain not selected")
	}
	journal.Entries[2].Key = "kubernetes/unknown"
	if p, _ := createRecoveryCheckpointProfile(journal); p != "" {
		t.Fatal("unknown write in pause chain accepted")
	}
}
