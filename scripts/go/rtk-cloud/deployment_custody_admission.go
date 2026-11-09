package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCustodyAdmissionRecord struct {
	Path    string   `json:"path"`
	Domain  string   `json:"domain,omitempty"`
	Subject string   `json:"subject"`
	Root    string   `json:"root_sha256"`
	Names   []string `json:"dns_names,omitempty"`
}

type environmentCustodyAdmissionBatch struct {
	ValidUntil time.Time                           `json:"valid_until"`
	Records    []environmentCustodyAdmissionRecord `json:"records"`
}

func environmentCustodyAdmissionRecords(binding *environmentCustodyBinding, owner environmentCustodyOwnerBinding, mqttRoot string) ([]environmentCustodyAdmissionRecord, error) {
	records := make([]environmentCustodyAdmissionRecord, 0, len(owner.Clients)+len(owner.Servers))
	for _, client := range owner.Clients {
		records = append(records, environmentCustodyAdmissionRecord{Path: client.StatePath, Subject: client.Subject, Root: binding.ServiceRootSHA256})
	}
	for _, server := range owner.Servers {
		root := binding.ServiceRootSHA256
		if server.Domain == "mqtt" {
			root = mqttRoot
		}
		names := strings.Split(server.DNSNames, ",")
		if server.Domain != "service" && server.Domain != "mqtt" || !slices.Contains(names, server.Name) {
			return nil, environmentCreateBootstrapBlocker("server custody admission requires its original domain and DNS policy")
		}
		records = append(records, environmentCustodyAdmissionRecord{Path: server.StatePath, Domain: server.Domain, Subject: server.Name, Root: root, Names: names})
	}
	return records, nil
}

func environmentCustodyAdmissionJob(cfg deploymentConfig, binding *environmentCustodyBinding, owner environmentCustodyOwnerBinding, source deploymentplan.Object, image, bootstrapRoot string, batch environmentCustodyAdmissionBatch) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, environmentCreateBootstrapBlocker("batch custody admission requires its original generation, owner and bounded credential references")
	}
	if binding == nil || source == nil || source["serviceAccountName"] != owner.ServiceAccount || len(owner.StorageAliases) == 0 || len(batch.Records) == 0 || len(batch.Records) > 16 || batch.ValidUntil.IsZero() {
		return bad()
	}
	if _, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, *binding, owner); err != nil {
		return bad()
	}
	alias := owner.StorageAliases[0]
	seen := map[string]bool{}
	for _, record := range batch.Records {
		ref, err := newEnvironmentCustodyStateReference(cfg, binding, owner.Namespace, alias, record.Path, record.Root)
		if err != nil || ref.Owner != owner.ID || seen[record.Path] {
			return bad()
		}
		seen[record.Path] = true
		switch record.Domain {
		case "":
			if len(record.Names) != 0 || record.Root != binding.ServiceRootSHA256 ||
				!slices.Contains(owner.Clients, environmentCustodyClientBinding{Subject: record.Subject, StatePath: record.Path}) {
				return bad()
			}
		case "service", "mqtt":
			if record.Domain == "service" && record.Root != binding.ServiceRootSHA256 ||
				!slices.Contains(owner.Servers, environmentCustodyServerBinding{Domain: record.Domain, Name: record.Subject, StatePath: record.Path, DNSNames: strings.Join(record.Names, ",")}) {
				return bad()
			}
		default:
			return bad()
		}
	}
	raw, err := json.Marshal(batch)
	if err != nil || len(raw) > 64<<10 {
		return bad()
	}
	// The command contains public references only. The native Job reads all
	// records through the original workload's scoped OpenBao authentication.
	return environmentCustodyScopedJob(cfg, binding, owner.Namespace, alias, batch.Records[0].Path, source, image, bootstrapRoot,
		[]any{"/app/serviceidentity-bootstrap", "inspect-admitted-batch", string(raw)}, false)
}

func runEnvironmentCustodyAdmission(ctx context.Context, b environmentKubernetesBackend, job deploymentplan.Object, namespace string, batch environmentCustodyAdmissionBatch) ([]environmentCustodyPublicState, error) {
	raw, err := runEnvironmentCustodyJob(ctx, b, job, namespace, nil, nil)
	if err != nil {
		return nil, err
	}
	var results []environmentCustodyPublicState
	if len(raw) > 64<<10 || decodeEnvironmentJSON(raw, &results) != nil || len(results) != len(batch.Records) {
		return nil, environmentCreateBootstrapBlocker("native batch admission returned incomplete public identity evidence")
	}
	for i, record := range batch.Records {
		result := results[i]
		if result.Subject != record.Subject || result.RootSHA256 != record.Root ||
			!environmentPreflightDigest.MatchString(result.Fingerprint) || !environmentPreflightDigest.MatchString(result.PublicKeySHA256) ||
			!environmentPreflightDigest.MatchString(result.StateSHA256) {
			return nil, environmentCreateBootstrapBlocker("native batch admission differs from its original logical identity")
		}
	}
	return results, nil
}

// Workload definitions supply the original scope and helper execution profile.
// Verification is independent of Pod readiness and uses no live private file.
func verifyEnvironmentCommittedCustody(runtime *deploymentCheckRuntime, until time.Time) error {
	return verifyEnvironmentCommittedCustodyWithOutputs(runtime, until, nil)
}

func verifyEnvironmentCommittedCustodyWithOutputs(runtime *deploymentCheckRuntime, until time.Time, outputs []environmentCreateProductIdentity) error {
	if runtime == nil || !runtime.custodyRequired {
		return nil
	}
	if runtime.custodyError != nil {
		return runtime.custodyError
	}
	r := runtime.custodyReader
	if r == nil {
		return environmentCreateBootstrapBlocker("selected OpenBao identity inspection is unavailable")
	}
	raw, err := r.command(runtime.ctx, nil, "get", "deployments", "--all-namespaces", "-o", "json")
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &list) != nil || list.Items == nil {
		return environmentCreateBootstrapBlocker("selected OpenBao workload inventory is unavailable")
	}
	generation, workloads, err := environmentCustodyAdmissionWorkloads(r.cfg, list.Items)
	if err != nil {
		return err
	}
	binding, err := readEnvironmentCustodyBootstrapBinding(r.cfg, r.store, generation)
	if err != nil {
		return err
	}
	mqttRoot, err := environmentCustodyAdmissionMQTTRoot(r.cfg, r.store, generation)
	if err != nil {
		return err
	}
	type inspection struct {
		job       deploymentplan.Object
		namespace string
		batch     environmentCustodyAdmissionBatch
	}
	jobs := make([]inspection, 0, len(binding.Owners))
	for _, owner := range binding.Owners {
		key := owner.Namespace + "/" + owner.ID
		source := workloads[key]
		delete(workloads, key)
		records, err := environmentCustodyAdmissionRecords(binding, owner, mqttRoot)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return &deploymentplan.Blocker{ID: "deployment.pki.custody-inspection", Resource: key, Cause: "original custody owner has no declared credential purpose"}
		}
		records = slices.DeleteFunc(records, func(record environmentCustodyAdmissionRecord) bool {
			return environmentCustodyAdmissionDeferred(owner, source, record, outputs)
		})
		if len(records) == 0 {
			continue
		}
		if source == nil {
			return &deploymentplan.Blocker{ID: "deployment.pki.custody-inspection", Resource: key, Cause: "original committed workload owner is missing from the selected scope"}
		}
		batch := environmentCustodyAdmissionBatch{ValidUntil: until.UTC(), Records: records}
		job, err := environmentCustodyAdmissionJob(r.cfg, binding, owner, source, r.image, "", batch)
		if err != nil {
			return err
		}
		jobs = append(jobs, inspection{job: job, namespace: owner.Namespace, batch: batch})
	}
	if len(jobs) == 0 || len(workloads) != 0 {
		return environmentCreateBootstrapBlocker("observed OpenBao workload scope does not match its original completed generation")
	}
	backend := r.backend
	backend.RolloutTimeout, err = environmentRolloutTimeout(r.cfg.Values)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(runtime.ctx)
	defer cancel()
	results := make([]error, len(jobs))
	next := make(chan int)
	var group sync.WaitGroup
	for range min(4, len(jobs)) {
		group.Go(func() {
			for i := range next {
				job := jobs[i]
				_, results[i] = runEnvironmentCustodyAdmission(ctx, backend, job.job, job.namespace, job.batch)
				if results[i] != nil {
					cancel()
				}
			}
		})
	}
dispatch:
	for i := range jobs {
		select {
		case next <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(next)
	group.Wait()
	for _, result := range results {
		if result != nil && !errors.Is(result, context.Canceled) {
			return result
		}
	}
	return ctx.Err()
}

// Only the normal-tail preflight supplies already validated producer outputs.
// A live registration reference always requires committed state, even on retry.
func environmentCustodyAdmissionDeferred(owner environmentCustodyOwnerBinding, source deploymentplan.Object, record environmentCustodyAdmissionRecord, outputs []environmentCreateProductIdentity) bool {
	if record.Domain != "" {
		return false
	}
	for _, output := range outputs {
		role := strings.TrimPrefix(output.Subject, "service:")
		if output.Subject != record.Subject || output.RootSHA256 != record.Root ||
			environmentCustodyProductWorkload(role) != owner.ID || !slices.Contains(owner.StorageAliases, output.Claim) ||
			environmentCreateServiceOwnerIdentityPath(role, "client.json") != record.Path {
			continue
		}
		for _, raw := range certIssuerObjectList(source["containers"]) {
			if mqttFoundationEnv(deploymentplan.Map(raw), "PKI_REGISTRATION_IDENTITY_STATE") != "" {
				return false
			}
		}
		return true
	}
	return false
}

func environmentCustodyAdmissionWorkloads(cfg deploymentConfig, deployments []deploymentplan.Object) (string, map[string]deploymentplan.Object, error) {
	generation := ""
	selected := map[string]deploymentplan.Object{}
	for _, deployment := range deployments {
		meta := deploymentplan.Map(deployment["metadata"])
		ns, name := deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["name"])
		if !strings.HasPrefix(ns, cfg.Values["CLOUD_STACK_NAME"]+"-") {
			continue
		}
		pod := environmentPKIPod(deployment)
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(raw)
			if mqttFoundationEnv(container, "PKI_STATE_BACKEND") != "openbao" {
				continue
			}
			prefix := mqttFoundationEnv(container, "PKI_STATE_RECORD_PREFIX")
			parts := strings.Split(prefix, "/")
			if len(parts) != 3 || parts[0] != cfg.Environment || !environmentPreflightDigest.MatchString(parts[1]) ||
				parts[2] != name || generation != "" && generation != parts[1] ||
				mqttFoundationEnv(container, "PKI_STATE_KV_MOUNT") != cfg.Values["PKI_STATE_KV_MOUNT"] ||
				mqttFoundationEnv(container, "PKI_STATE_OPENBAO_ROLE") != environmentCustodyRoleName(prefix) ||
				mqttFoundationEnv(container, "PKI_ENVIRONMENT") != cfg.Environment {
				return "", nil, environmentCreateBootstrapBlocker("observed workload refers to a different OpenBao custody scope")
			}
			generation = parts[1]
			key := ns + "/" + name
			if selected[key] == nil {
				// A multi-container workload may assign a different UID to
				// its PKI sidecar. Reuse its profile for the read-only helper.
				source := deploymentplan.Clone(pod)
				containers := []any{deploymentplan.Clone(container)}
				for _, other := range certIssuerObjectList(pod["containers"]) {
					if deploymentplan.Map(other)["name"] != container["name"] {
						containers = append(containers, other)
					}
				}
				source["containers"] = containers
				if source["securityContext"] == nil {
					source["securityContext"] = environmentCreateServicePodSecurity()
				}
				selected[key] = source
			}
		}
	}
	if generation == "" {
		return "", nil, environmentCreateBootstrapBlocker("original OpenBao custody generation is missing from the selected workload inventory")
	}
	return generation, selected, nil
}

func environmentCustodyAdmissionMQTTRoot(cfg deploymentConfig, store secretStore, generation string) (string, error) {
	path, err := store.safePath(filepath.Join("deployment", "create", generation, "bootstrap/mqtt/root-active.json"))
	if err != nil {
		return "", err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var document deploymentplan.Object
	if err != nil || decodeEnvironmentJSON(raw, &document) != nil || document["environment"] != cfg.Environment ||
		document["trust_domain"] != "mqtt" || document["kind"] != "root" || document["status"] != "active" ||
		!environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(document["issuer_id"])) {
		return "", environmentCreateBootstrapBlocker("original activated MQTT authority is unavailable")
	}
	cert, err := parseFirstCertificatePEM([]byte(deploymentplan.String(document["certificate_pem"])))
	if err != nil || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil ||
		environmentPreflightBytesSHA256(cert.Raw) != document["certificate_fingerprint_sha256"] {
		return "", environmentCreateBootstrapBlocker("original activated MQTT authority has invalid public evidence")
	}
	return environmentPreflightBytesSHA256(cert.Raw), nil
}
