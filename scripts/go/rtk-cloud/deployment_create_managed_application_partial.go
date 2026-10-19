package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedApplicationPartialRecovery struct {
	Interrupted                    *createManagedApplicationInterruptedForward `json:"Interrupted,omitempty"`
	Review, SourceSHA, ArtifactSHA string
	Plan                           *deploymentplan.Plan
	Journal                        deploymentplan.Journal
	AcceptedPlans                  map[string]*deploymentplan.Plan
	AcceptedJournals               map[string]deploymentplan.Journal
	ExportProofSHAs                map[string]string
	Exports                        map[string]*createManagedTransportExportRetire
	Historical                     *createManagedTransportPartialRecovery
	LedgerSourceSHA                string
	NewTrustVolumes                map[string]environmentCreateVolumeOwner
}

func managedApplicationPartialReview(j environmentCreateJournal) string {
	if len(j.Entries) < 13 || !environmentPreflightDigest.MatchString(j.PlanSHA256) {
		return ""
	}
	tail := j.Entries[len(j.Entries)-10:]
	const prefix = "openbao-recovery-admission-"
	review := strings.TrimPrefix(tail[0].Key, prefix)
	if !strings.HasPrefix(tail[0].Key, prefix) || !environmentPreflightDigest.MatchString(review) {
		return ""
	}
	keys := []string{"openbao-recovery-admission-" + review, "kubernetes/retire-pki-export-video-cloud-api-pki-" + j.PlanSHA256[:12], "kubernetes/server-secret-video-cloud-api-pki", "kubernetes/server-enrollment-video-cloud-api-app-pki", "kubernetes/server-custody-video-cloud-api-app-pki", "server-export-video-cloud-api-app-pki", "retire-pki-export-video-cloud-api-app-pki-" + j.PlanSHA256[:12], "kubernetes/retire-pki-export-video-cloud-api-app-pki-" + j.PlanSHA256[:12], "kubernetes/server-secret-video-cloud-api-app-pki", "kubernetes/managed-application-owners"}
	seen := map[string]bool{}
	for index, entry := range j.Entries {
		if seen[entry.Key] || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return ""
		}
		seen[entry.Key] = true
		if index == len(j.Entries)-1 {
			if entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
				return ""
			}
		} else if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			return ""
		}
		if index >= len(j.Entries)-10 && entry.Key != keys[index-len(j.Entries)+10] {
			return ""
		}
	}
	before := j
	before.Entries = slices.Clone(j.Entries[:len(j.Entries)-10])
	retire := &before.Entries[len(before.Entries)-1]
	if !strings.HasPrefix(retire.Key, "retire-pki-export-video-cloud-api-pki-") {
		return ""
	}
	retire.State, retire.CompletedAt, retire.Receipt = "pending", environmentCreateJournalEntry{}.CompletedAt, nil
	if managedTransportExportRetirementReview(before) == "" {
		return ""
	}
	return review
}

func validateManagedApplicationPartialJournal(plan *deploymentplan.Plan, journal deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("managed application partial requires its exact twenty-two completed resources, written API and three untouched hosts")
	}
	if plan == nil || len(plan.Resources) != 26 || journal.PlanSHA256 != plan.SHA256() || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "Deployment/"+plan.Stack+"-video-cloud/video-cloud-api" || len(journal.Completed) != 22 || len(journal.Owners) != 23 {
		return bad()
	}
	namespace := plan.Stack + "-video-cloud"
	ids := managedApplicationPartialResourceIDs(namespace)
	actions := []string{"create", "create", "create", "create", "update", "update", "create", "update", "create", "create", "create", "create", "retain", "retain", "retain", "retain", "create", "retain", "create", "create", "create", "update", "update", "update", "update", "update"}
	for index, resource := range plan.Resources {
		phase := "configuration"
		if index < 2 {
			phase = "storage"
		} else if index < 4 {
			phase = "bootstrap"
		} else if index >= 20 {
			phase = "workload"
		}
		if resource.Phase != phase || resource.ID != ids[index] || resource.Action != actions[index] || deploymentplan.ID(resource.Desired) != resource.ID || !slices.Contains([]string{"create", "update", "retain"}, resource.Action) || (resource.Action == "create" && (resource.UID != "" || resource.Baseline != nil)) || (resource.Action != "create" && (resource.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(resource.UID))) {
			return bad()
		}
		owner := journal.Owners[resource.ID]
		if index < 22 {
			if journal.Completed[index] != resource.ID || !environmentBootstrapOwnerUUID.MatchString(owner) || resource.UID != "" && owner != resource.UID {
				return bad()
			}
		} else if index == 22 {
			if resource.ID != journal.Failed || resource.Action != "update" || resource.Baseline == nil || resource.UID == "" || owner != resource.UID {
				return bad()
			}
		} else {
			names := []string{"video-cloud-api-app-pki", "video-cloud-api-pki", "video-cloud-logingester"}
			if resource.ID != "Deployment/"+plan.Stack+"-video-cloud/"+names[index-23] || resource.Action != "update" || resource.Baseline == nil || resource.UID == "" || owner != "" {
				return bad()
			}
		}
	}
	return nil
}

func loadManagedApplicationPartialSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedApplicationPartialRecovery, error) {
	var empty createFoundationRecoveryArtifact
	bad := func() (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedApplicationPartialRecovery, error) {
		return nil, empty, nil, createProviderRecoveryBlock("managed application continuation requires its admitted export-retirement source and exact original partial application journal")
	}
	review := managedApplicationPartialReview(e.Journal)
	if review == "" {
		return bad()
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != "rtk-cloud.create-managed-transports-partial-recovery/v1" || !environmentPreflightDigest.MatchString(a.Plan.Materials["accepted-service-core-managed-partial-native-proof"]) || managedTransportExportRetirementReview(source) == "" || len(e.Journal.Entries) != len(source.Entries)+10 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+7 {
		return bad()
	}
	before := e.Journal
	before.Entries = slices.Clone(e.Journal.Entries[:len(source.Entries)])
	old, current := source.Entries[len(source.Entries)-1], before.Entries[len(before.Entries)-1]
	var retired deploymentplan.Object
	if current.Key != old.Key || current.State != "completed" || current.CompletedAt.IsZero() || json.Unmarshal(current.Receipt, &retired) != nil || retired["absent"] != true || deploymentplan.String(retired["resource_id"]) != "Deployment/"+source.Stack+"-video-cloud/pki-export-video-cloud-api-pki-"+source.PlanSHA256[:12] || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(retired["uid"])) {
		return bad()
	}
	checked := current
	checked.State, checked.CompletedAt, checked.Receipt = old.State, old.CompletedAt, old.Receipt
	if activeTransportJournalDigest(checked) != activeTransportJournalDigest(old) {
		return bad()
	}
	before.Entries[len(before.Entries)-1] = old
	before.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(before) != deploymentplan.Digest(source) {
		return bad()
	}
	historical := *e
	historical.Journal = source
	lastLease := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
	sha := strings.SplitN(lastLease.Holder, ":", 2)[0]
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	path := filepath.Join(store.Root, "deployment/rollback", sha)
	if !environmentPreflightDigest.MatchString(sha) || lastLease.UID == "" || lastLease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || readExecutionJSON(filepath.Join(path, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(path, "journal.json"), &journal) != nil || plan.SHA256() != sha || validateManagedApplicationPartialJournal(&plan, journal) != nil {
		return bad()
	}
	intent, intentErr := environmentCreateKubernetesStageIntent(&plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	if intentErr != nil || hashErr != nil || hash != e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 {
		return bad()
	}
	partial := &createManagedApplicationPartialRecovery{Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &plan, Journal: journal, AcceptedPlans: map[string]*deploymentplan.Plan{}, AcceptedJournals: map[string]deploymentplan.Journal{}, ExportProofSHAs: map[string]string{}, Exports: map[string]*createManagedTransportExportRetire{}, LedgerSourceSHA: a.Plan.Materials["accepted-service-core-managed-partial-original-volume-ledger"], NewTrustVolumes: map[string]environmentCreateVolumeOwner{}}
	index := len(source.KubernetesLeases)
	for _, entry := range e.Journal.Entries[len(source.Entries) : len(e.Journal.Entries)-1] {
		if !strings.HasPrefix(entry.Key, "kubernetes/") {
			partial.ExportProofSHAs[entry.Key] = activeTransportJournalDigest(entry)
			continue
		}
		accepted, child, err := managedTransportPostAcceptedStage(store, e, entry.Key)
		if err != nil || len(accepted.Resources) != 1 {
			return bad()
		}
		lease := e.Journal.KubernetesLeases[index]
		if lease.UID == "" || lease.ResourceID != lastLease.ResourceID || strings.SplitN(lease.Holder, ":", 2)[0] != accepted.SHA256() {
			return bad()
		}
		partial.AcceptedPlans[entry.Key], partial.AcceptedJournals[entry.Key] = accepted, child
		index++
	}
	if index != len(e.Journal.KubernetesLeases)-1 {
		return bad()
	}
	return &historical, a, partial, nil
}

func managedApplicationPartialResourceIDs(namespace string) []string {
	names := []string{"PersistentVolumeClaim/video-cloud-api-app-pki-trust", "PersistentVolumeClaim/video-cloud-api-pki-trust", "Job/prepare-video-cloud-api-app-pki-trust", "Job/prepare-video-cloud-api-pki-trust", "ConfigMap/pki-create-app-trust", "ConfigMap/pki-create-device-trust", "ConfigMap/pki-create-mqtt-callback-ca", "ConfigMap/pki-create-mqtt-consumer-trust", "ConfigMap/pki-create-service-consumer-crls", "NetworkPolicy/allow-mqtt-pki", "NetworkPolicy/allow-video-cloud-api-app-pki", "NetworkPolicy/allow-video-cloud-api-pki", "Secret/factoryenroll-runtime", "Secret/video-cloud-runtime", "Secret/video-cloud-workers-runtime", "Service/certissuer", "Service/mqtt-pki", "Service/pki-controller", "Service/video-cloud-api-app-pki", "Service/video-cloud-api-pki", "Deployment/factoryenroll", "Deployment/mqtt-pki", "Deployment/video-cloud-api", "Deployment/video-cloud-api-app-pki", "Deployment/video-cloud-api-pki", "Deployment/video-cloud-logingester"}
	ids := make([]string, len(names))
	for index, name := range names {
		pair := strings.SplitN(name, "/", 2)
		ids[index] = pair[0] + "/" + namespace + "/" + pair[1]
	}
	return ids
}

func validateManagedApplicationPartialExports(store secretStore, e, source *environmentCreateExecution, partial *createManagedApplicationPartialRecovery, root environmentCreateRegisteredIssuer) error {
	bad := func(stage string) error {
		return createProviderRecoveryBlock("managed application retained server source differs: stage=" + stage)
	}
	if partial == nil || partial.Historical == nil || partial.Historical.PostManaged == nil || partial.Historical.PostManaged.ExportRetire == nil {
		return bad("original-api-export")
	}
	original := partial.Historical.PostManaged.ExportRetire
	oldRetired := e.Journal.Entries[len(source.Journal.Entries)-1]
	var oldReceipt deploymentplan.Object
	if json.Unmarshal(oldRetired.Receipt, &oldReceipt) != nil || len(oldReceipt) != 3 || oldReceipt["absent"] != true || deploymentplan.String(oldReceipt["uid"]) != original.ReaderUID || oldRetired.IntentSHA256 != activeTransportJournalDigest(deploymentplan.Object{"resource_id": "Deployment/" + e.Journal.Stack + "-video-cloud/pki-export-video-cloud-api-pki-" + e.Journal.PlanSHA256[:12], "uid": original.ReaderUID}) {
		return bad("original-api-retirement")
	}
	partial.Exports["video-cloud-api-pki"] = original
	// Both TLS Secrets are completed stages. Their private bytes remain inside the
	// qualified private plan and are compared to genuine native copies by preflight.
	first := partial.AcceptedPlans["kubernetes/retire-pki-export-video-cloud-api-pki-"+e.Journal.PlanSHA256[:12]]
	if first == nil || first.Resources[0].ID != deploymentplan.String(oldReceipt["resource_id"]) || first.Resources[0].Action != "retire" || first.Resources[0].UID != original.ReaderUID {
		return bad("original-api-retirement-child")
	}
	session := partial.Historical.PostManaged.Session
	ns := e.Journal.Stack + "-video-cloud"
	expected := map[string]string{
		"kubernetes/server-secret-video-cloud-api-pki":                                      "Secret/" + ns + "/video-cloud-api-pki-tls",
		"kubernetes/server-enrollment-video-cloud-api-app-pki":                              "Job/" + ns + "/pki-enroll-video-cloud-api-app-pki-server-" + strings.ReplaceAll(session, "-", "")[:12],
		"kubernetes/server-custody-video-cloud-api-app-pki":                                 "Deployment/" + ns + "/pki-export-video-cloud-api-app-pki-" + e.Journal.PlanSHA256[:12],
		"kubernetes/retire-pki-export-video-cloud-api-app-pki-" + e.Journal.PlanSHA256[:12]: "Deployment/" + ns + "/pki-export-video-cloud-api-app-pki-" + e.Journal.PlanSHA256[:12],
		"kubernetes/server-secret-video-cloud-api-app-pki":                                  "Secret/" + ns + "/video-cloud-api-app-pki-tls",
	}
	for key, id := range expected {
		plan := partial.AcceptedPlans[key]
		if plan == nil || plan.Resources[0].ID != id {
			return bad("accepted-owner-" + key)
		}
	}
	tail := e.Journal.Entries[len(source.Journal.Entries):]
	export, retire := tail[5], tail[6]
	var proof struct {
		Certificate string `json:"certificate_sha256"`
		Claim       string `json:"claim_uid"`
		Reader      string `json:"reader_uid"`
		Pod         string `json:"pod_uid"`
		ReplicaSet  string `json:"replicaset_uid"`
		Name        string `json:"server_name"`
	}
	if json.Unmarshal(export.Receipt, &proof) != nil || !environmentPreflightDigest.MatchString(proof.Certificate) || !environmentBootstrapOwnerUUID.MatchString(proof.Claim) || !environmentBootstrapOwnerUUID.MatchString(proof.Pod) || !environmentBootstrapOwnerUUID.MatchString(proof.ReplicaSet) || proof.Name != "video-cloud-api-app-pki."+ns+".svc" {
		return bad("app-api-export-receipt")
	}
	custodyKey := "kubernetes/server-custody-video-cloud-api-app-pki"
	custody := partial.AcceptedPlans[custodyKey].Resources[0]
	if proof.Reader != partial.AcceptedJournals[custodyKey].Owners[custody.ID] {
		return bad("app-api-reader-owner")
	}
	intent := deploymentplan.Object{"owner": "video-cloud-api-app-pki", "namespace": ns, "claim_uid": proof.Claim, "reader_uid": proof.Reader, "pod_uid": proof.Pod, "replicaset_uid": proof.ReplicaSet, "server_name": proof.Name, "root_sha256": root.RootFingerprint}
	retireIntent := deploymentplan.Object{"resource_id": custody.ID, "uid": proof.Reader}
	var receipt deploymentplan.Object
	if export.Phase != "bootstrap" || export.IntentSHA256 != activeTransportJournalDigest(intent) || retire.Phase != "bootstrap" || retire.IntentSHA256 != activeTransportJournalDigest(retireIntent) || json.Unmarshal(retire.Receipt, &receipt) != nil || len(receipt) != 3 || receipt["absent"] != true || receipt["resource_id"] != custody.ID || receipt["uid"] != proof.Reader {
		return bad("app-api-export-retirement-intents")
	}
	retirement := partial.AcceptedPlans["kubernetes/"+retire.Key].Resources[0]
	if retirement.Action != "retire" || retirement.UID != proof.Reader || !deploymentplan.Matches(custody.Desired, retirement.Baseline) {
		return bad("app-api-retirement-child")
	}
	app := &createManagedTransportExportRetire{ExportSHA: activeTransportJournalDigest(export), RetireSHA: activeTransportJournalDigest(retire), CertificateSHA: proof.Certificate, ClaimUID: proof.Claim, ReaderUID: proof.Reader, PodUID: proof.Pod, ReplicaSetUID: proof.ReplicaSet, ServerName: proof.Name, RootSHA: root.RootFingerprint}
	partial.Exports["video-cloud-api-app-pki"] = app
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		plan := partial.AcceptedPlans["kubernetes/server-secret-"+name]
		resource := plan.Resources[0]
		if resource.Action != "create" || resource.UID != "" || resource.Baseline != nil || deploymentplan.String(resource.Desired["type"]) != "Opaque" {
			return bad("serving-secret-owner-" + name)
		}
		data := deploymentplan.Map(resource.Desired["data"])
		cert, certErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.crt"]))
		key, keyErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.key"]))
		ca, caErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["ca.crt"]))
		pair, pairErr := tls.X509KeyPair(cert, key)
		if certErr != nil || keyErr != nil || caErr != nil || pairErr != nil || len(pair.Certificate) < 2 || environmentPreflightBytesSHA256(cert) != partial.Exports[name].CertificateSHA || string(ca) != root.Certificate || len(data) != 3 {
			return bad("serving-secret-material-" + name)
		}
	}
	return nil
}

func managedApplicationPartialNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	source, _, partial, err := loadManagedApplicationPartialSource(store, e)
	if err != nil {
		return false
	}
	base, prior, accepted, post, err := loadManagedTransportExportRetirementSource(store, source, true)
	if err != nil {
		return false
	}
	original, journal, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil {
		return false
	}
	partial.Historical = &createManagedTransportPartialRecovery{Original: original, Journal: journal, PriorForward: prior, AcceptedForward: accepted, PostManaged: post}
	var publication struct {
		Root         map[string]any `json:"root"`
		Intermediate map[string]any `json:"intermediate"`
	}
	if readExecutionJSON(filepath.Join(store.Root, "pki/service/issuer.json"), &publication) != nil {
		return false
	}
	root, err := environmentCreateRegisteredIssuerFromMap(publication.Root, "")
	return err == nil && validateManagedApplicationPartialExports(store, e, source, partial, root) == nil
}
