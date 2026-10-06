package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
)

// Reinitialization records discarded data, not an empty migration. Its receipt
// never authorizes data rollback to a source which no longer exists.
type storageReinitializationIdentity struct {
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	Endpoint string `json:"endpoint"`
	Prefix   string `json:"prefix"`
}

type storageReinitializationProof struct {
	Environment                 string                          `json:"environment"`
	Purpose                     string                          `json:"purpose"`
	Source                      storageReinitializationIdentity `json:"source"`
	Destination                 storageReinitializationIdentity `json:"destination"`
	SourceProfileSHA256         string                          `json:"source_profile_sha256"`
	DestinationProfileSHA256    string                          `json:"destination_profile_sha256"`
	AcknowledgedDiscardedSource string                          `json:"acknowledged_discarded_source"`
	SourceAbsent                bool                            `json:"source_absent"`
	DestinationEmpty            bool                            `json:"destination_empty"`
	CanaryVerified              bool                            `json:"destination_write_read_delete_canary"`
	DestinationKeyID            int                             `json:"destination_key_id"`
	CheckedAt                   time.Time                       `json:"checked_at"`
	EmptyEndpointConsumers      []string                        `json:"repaired_empty_endpoint_consumers,omitempty"`
}

type storageReinitializationReceipt struct {
	Operation             string    `json:"operation"`
	Environment           string    `json:"environment"`
	Purpose               string    `json:"purpose"`
	Bucket                string    `json:"bucket"`
	Region                string    `json:"region"`
	Prefix                string    `json:"prefix"`
	CutoverID             string    `json:"cutover_id"`
	CutoverAt             time.Time `json:"cutover_at"`
	ProofSHA256           string    `json:"reinitialization_proof_sha256"`
	SourceDataDiscarded   bool      `json:"source_data_discarded"`
	DataRollbackAvailable bool      `json:"data_rollback_available"`
	ServiceReady          bool      `json:"service_ready"`
}

func isStorageReinitializationReceipt(body []byte) bool {
	var r struct {
		Operation string `json:"operation"`
	}
	return json.Unmarshal(body, &r) == nil && r.Operation == "reinitialize"
}

func storageReinitializationHash(value any) string {
	body, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func validateStorageReinitializationReceipt(environment, purpose string, target deploymentStorageTarget, body []byte) error {
	var receipt storageReinitializationReceipt
	if json.Unmarshal(body, &receipt) != nil || receipt.Operation != "reinitialize" || receipt.Environment != environment || receipt.Purpose != purpose || receipt.Bucket != target.Bucket || receipt.Region != target.Region || receipt.Prefix != target.Prefix || receipt.CutoverID == "" || receipt.ProofSHA256 == "" || !receipt.SourceDataDiscarded || receipt.DataRollbackAvailable || receipt.CutoverAt.IsZero() || receipt.CutoverAt.After(time.Now()) || (purpose == "ota" && !receipt.ServiceReady) {
		return errors.New("reinitialization receipt does not match the selected storage target")
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	raw, err := store.read(storageCutoverJournalName(purpose))
	if err != nil {
		return fmt.Errorf("private reinitialization journal is required: %w", err)
	}
	var journal storageCutoverJournal
	if json.Unmarshal([]byte(raw), &journal) != nil || journal.Operation != "reinitialize" || journal.Status != "complete" || journal.Environment != environment || journal.Purpose != purpose || journal.ID != receipt.CutoverID || journal.MigrationSHA256 != "" || journal.Reinitialization == nil || storageReinitializationHash(journal.Reinitialization) != receipt.ProofSHA256 {
		return errors.New("reinitialization receipt does not match its completed private journal")
	}
	p := journal.Reinitialization
	if p.Environment != environment || p.Purpose != purpose || p.Destination.Bucket != target.Bucket || p.Destination.Region != target.Region || p.Destination.Prefix != target.Prefix || p.Source.Bucket == "" || p.Source.Bucket == p.Destination.Bucket || p.AcknowledgedDiscardedSource != p.Source.Bucket || !p.SourceAbsent || !p.DestinationEmpty || !p.CanaryVerified || p.DestinationKeyID <= 0 || p.CheckedAt.IsZero() || p.CheckedAt.After(receipt.CutoverAt) || p.SourceProfileSHA256 == "" || p.DestinationProfileSHA256 == "" || journal.ClusterUID == "" || len(journal.Mutations) == 0 {
		return errors.New("reinitialization journal lacks valid discarded-source and empty-destination proof")
	}
	return nil
}

func readStorageReinitializationSource(path, environment string) (provisionObjectStore, string, error) {
	if path == "" {
		return provisionObjectStore{}, "", errors.New("--source-env-file is required for reinitialization")
	}
	values, profileHash, err := readStorageReinitializationProfile(path)
	if err != nil {
		return provisionObjectStore{}, "", err
	}
	if values["RTK_STORAGE_SOURCE_ENVIRONMENT"] != environment {
		return provisionObjectStore{}, "", errors.New("source profile requires RTK_STORAGE_SOURCE_ENVIRONMENT matching the selected environment")
	}
	source := provisionObjectStore{bucket: values["LINODE_OBJ_BUCKET"], region: values["LINODE_OBJ_REGION"], endpoint: strings.TrimRight(values["LINODE_OBJ_ENDPOINT"], "/")}
	source.prefix, source.prefixSet = values["LINODE_OBJ_PREFIX"]
	if source.bucket == "" || source.region == "" || source.endpoint == "" || !source.prefixSet {
		return source, "", errors.New("source profile must explicitly bind bucket, region, endpoint, and prefix; deleted source credentials are not required")
	}
	if source.prefix != "" {
		for _, part := range strings.Split(source.prefix, "/") {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\") || strings.IndexFunc(part, unicode.IsSpace) >= 0 || strings.IndexFunc(part, unicode.IsControl) >= 0 {
				return source, "", errors.New("source prefix contains empty, traversal, whitespace, or ambiguous segments; preserve and review its exact legacy mapping")
			}
		}
	}
	endpoint, err := url.Parse(source.endpoint)
	if err != nil || endpoint == nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return source, "", errors.New("source endpoint must be an explicit S3 origin without credentials, query, or path")
	}
	if _, err := normalizeLinodeS3Endpoint(source.endpoint); err != nil {
		return source, "", err
	}
	return source, profileHash, nil
}

func (c deploymentCredentialChecker) requireStorageSourceAbsent(token string, source provisionObjectStore) error {
	var buckets []linodeStorageBucket
	if err := c.storageAccountInventory(token, "buckets", &buckets); err != nil {
		return fmt.Errorf("cannot prove source absence: %w", err)
	}
	for _, bucket := range buckets {
		if bucket.Label == source.bucket {
			return errors.New("source bucket still exists; use the migration workflow instead of reinitialization")
		}
	}
	return nil
}

func (c deploymentCredentialChecker) requireEmptyStorageDestination(store provisionObjectStore) error {
	for _, query := range []string{"objects", "versions", "uploads"} {
		q := url.Values{query: {""}}
		root := map[string]string{"objects": "ListBucketResult", "versions": "ListVersionsResult", "uploads": "ListMultipartUploadsResult"}[query]
		if query == "objects" {
			q = url.Values{"list-type": {"2"}, "max-keys": {"1"}}
		}
		body, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", q, nil)
		if err != nil {
			return fmt.Errorf("cannot prove empty destination %s: %w", query, err)
		}
		var result struct {
			XMLName   xml.Name
			Name      *string    `xml:"Name"`
			Bucket    string     `xml:"Bucket"`
			Truncated *bool      `xml:"IsTruncated"`
			Contents  []struct{} `xml:"Contents"`
			Versions  []struct{} `xml:"Version"`
			Markers   []struct{} `xml:"DeleteMarker"`
			Uploads   []struct{} `xml:"Upload"`
		}
		if xml.Unmarshal(body, &result) != nil || result.XMLName.Local != root || result.Truncated == nil {
			return fmt.Errorf("invalid destination %s inventory identity or XML", query)
		}
		identityMatches := result.Name != nil && *result.Name == store.bucket
		if query == "uploads" {
			identityMatches = result.Bucket == store.bucket
		} else if query == "versions" && result.Name != nil && *result.Name == "" {
			// Linode E3 returns an explicit empty Name for an empty version list.
			// This routine still requires exact bucket identities from both the
			// object and multipart listings using this same endpoint and key.
			identityMatches = true
		}
		if !identityMatches {
			return fmt.Errorf("invalid destination %s inventory identity or XML", query)
		}
		if *result.Truncated || len(result.Contents)+len(result.Versions)+len(result.Markers)+len(result.Uploads) != 0 {
			return fmt.Errorf("reinitialization requires a completely empty destination; %s inventory is nonempty or incomplete", query)
		}
	}
	return nil
}

func storageReinitializationTarget(cfg deploymentConfig, purpose string) (deploymentStorageTarget, string, error) {
	target, prefix, label := cfg.Storage.RuntimeMedia, "LINODE_MEDIA_OBJ_", "runtime"
	if purpose == "ota" {
		target, prefix, label = cfg.Storage.OTAFirmware, "LINODE_OTA_OBJ_", "ota-firmware"
	} else if purpose != "media" {
		return target, "", errors.New("storage-reinitialize supports only media and ota")
	}
	if target.Region == "" || target.Bucket != "rtk-cloud-"+cfg.Environment+"-"+label+"-"+target.Region || target.Prefix != "environments/"+cfg.Values["CLOUD_STACK_NAME"] {
		return target, "", errors.New("reinitialization destination must use the canonical environment bucket and prefix")
	}
	if purpose == "ota" && cfg.Storage.OTAMode != "dedicated" {
		return target, "", errors.New("OTA reinitialization requires dedicated storage")
	}
	return target, prefix, nil
}

func runStorageReinitialize(cfg deploymentConfig, purpose, sourceFile, candidateFile, acknowledged string, plan bool) error {
	return defaultDeploymentCredentialChecker().reinitializeStorage(cfg, purpose, sourceFile, candidateFile, acknowledged, plan)
}

func (c deploymentCredentialChecker) reinitializeStorage(cfg deploymentConfig, purpose, sourceFile, candidateFile, acknowledged string, plan bool) (result error) {
	target, credentialPrefix, err := storageReinitializationTarget(cfg, purpose)
	if err != nil {
		return err
	}
	source, sourceHash, err := readStorageReinitializationSource(sourceFile, cfg.Environment)
	if err != nil {
		return err
	}
	if acknowledged != source.bucket || source.bucket == target.Bucket {
		return errors.New("--acknowledge-discarded-source must name the exact deleted source bucket, distinct from the destination")
	}
	candidate, candidateHash, err := readStorageReinitializationProfile(candidateFile)
	if err != nil || candidate["RTK_STORAGE_CANDIDATE_ENVIRONMENT"] != cfg.Environment {
		return errors.New("reinitialization requires the selected environment's private candidate profile")
	}
	access, secret := candidate[credentialPrefix+"ACCESS_KEY_ID"], candidate[credentialPrefix+"SECRET_ACCESS_KEY"]
	if access == "" || secret == "" {
		return errors.New("explicit purpose-scoped candidate credentials are required")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	active, err := store.readOperator()
	if err != nil {
		return err
	}
	token := active["LINODE_TOKEN"]
	selectedEnv := appendMap(appendMap(cfg.Values, cfg.AdapterValues), active)
	selectedEnv["CLOUD_STACK_NAME"] = cfg.Values["CLOUD_STACK_NAME"]
	if token == "" {
		return errors.New("selected environment LINODE_TOKEN is required")
	}
	if err := c.requireStorageSourceAbsent(token, source); err != nil {
		return err
	}
	bucket, err := c.resolveStorageBucket(token, target)
	if err != nil {
		return err
	}
	target.Endpoint, err = normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	key, err := c.resolveAuthorizedStorageKey(token, access, target)
	if err != nil {
		return err
	}
	destination := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: target.Endpoint, accessKey: access, secretKey: secret}
	if err := c.validateStoragePrivacy(destination); err != nil {
		return err
	}
	if err := c.requireEmptyStorageDestination(destination); err != nil {
		return err
	}
	if purpose == "ota" {
		if err := validateOTADirectMetricsEndpoint(bucket); err != nil {
			return err
		}
		registration := strings.ToLower(strings.TrimSpace(selectedEnv["LKE_OTA_SERVICE_REGISTRATION_ENABLED"]))
		if registration != "true" && registration != "1" && registration != "yes" && registration != "on" {
			return errors.New("OTA service registration is disabled; reinitialization cannot activate dedicated OTA")
		}
		if err := lkeValidateOTACDNBaseURL(selectedEnv); err != nil {
			return err
		}
	}
	restore, err := storageCutoverKubeconfig(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	var otaBoundary storageReinitializationOTABoundary
	var mediaIsolation *storageReinitializationMediaIsolation
	if purpose == "media" && cfg.Storage.OTAMode == "dedicated" {
		mediaIsolation = &storageReinitializationMediaIsolation{stack: cfg.Values["CLOUD_STACK_NAME"], sourceBucket: source.bucket}
	}
	inspect := func() ([]byte, error) {
		body, err := kubectlCombinedOutput(nil, "get", "deployments,statefulsets,daemonsets,jobs,cronjobs,replicasets,pods", "-A", "-o", "json")
		if err != nil {
			return nil, err
		}
		if purpose == "ota" {
			body, err = storageReinitializationOTAInventory(body, cfg.Values["CLOUD_STACK_NAME"], source.bucket)
			if err != nil {
				return nil, err
			}
			if err := validateStorageReinitializationOTADelivery(body, selectedEnv); err != nil {
				return nil, err
			}
			if err := otaBoundary.verify(body, selectedEnv); err != nil {
				return nil, err
			}
			if selectedEnv["VIDEO_CLOUD_OTA_CDN_BASE_URL"] == "" {
				if err := validateOTAMetricsQualification(cfg.RuntimeRoot, cfg.Environment, target.Bucket, target.Region, target.Endpoint, time.Now().UTC()); err != nil {
					return nil, err
				}
			}
			return body, nil
		}
		if mediaIsolation != nil {
			return mediaIsolation.filter(body)
		}
		return body, nil
	}
	body, err := inspect()
	if err != nil {
		return err
	}
	emptyEndpoints, err := c.verifyReinitializationEmptyEndpoints(token, body, source)
	if err != nil {
		return err
	}
	source.reinitializeEmptyEndpoint = len(emptyEndpoints) > 0
	mutations, snapshots, err := planStorageConsumers(body, cfg.Values["CLOUD_STACK_NAME"], source, target, access, secret, purpose)
	if err != nil {
		return err
	}
	if len(mutations) == 0 {
		return errors.New("no existing source-bound consumers found; refusing an activation receipt")
	}
	if mediaIsolation != nil {
		if err := mediaIsolation.protectDestinationSecrets(mutations); err != nil {
			return err
		}
	}
	proof := storageReinitializationProof{Environment: cfg.Environment, Purpose: purpose, Source: storageReinitializationIdentity{source.bucket, source.region, source.endpoint, source.prefix}, Destination: storageReinitializationIdentity{target.Bucket, target.Region, target.Endpoint, target.Prefix}, SourceProfileSHA256: sourceHash, DestinationProfileSHA256: candidateHash, AcknowledgedDiscardedSource: acknowledged, SourceAbsent: true, DestinationEmpty: true, DestinationKeyID: key.ID, CheckedAt: time.Now().UTC(), EmptyEndpointConsumers: emptyEndpoints}
	if err := verifyStorageReinitializationProfiles(proof, sourceFile, candidateFile); err != nil {
		return err
	}
	consumers := []string{}
	for _, m := range mutations {
		consumers = append(consumers, m.Kind+"/"+m.Namespace+"/"+m.Name)
	}
	if plan {
		b, _ := json.MarshalIndent(map[string]any{"operation": "reinitialize", "plan": true, "proof": proof, "consumers": consumers, "images": "preserve existing images", "data_rollback_available": false}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	// Repeat provider proofs immediately before changes. No source S3 request is
	// made: a denied old credential is never accepted as proof of absence.
	if err := c.validateStorageReadWriteCanary(destination, target.Prefix); err != nil {
		return fmt.Errorf("destination canary failed before consumer activation: %w", err)
	}
	if err := c.requireStorageSourceAbsent(token, source); err != nil {
		return err
	}
	if err := c.requireEmptyStorageDestination(destination); err != nil {
		return err
	}
	if err := c.validateStoragePrivacy(destination); err != nil {
		return err
	}
	proof.CanaryVerified = true
	proof.CheckedAt = time.Now().UTC()
	freshSource, err := os.ReadFile(sourceFile)
	if err != nil {
		return err
	}
	freshCandidate, err := os.ReadFile(candidateFile)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(freshSource)) != sourceHash || fmt.Sprintf("%x", sha256.Sum256(freshCandidate)) != proof.DestinationProfileSHA256 {
		return errors.New("storage profiles changed during reinitialization planning")
	}
	cluster, err := storageCutoverRead("namespace", "", "kube-system")
	if err != nil {
		return err
	}
	clusterUID := storageCutoverString(storageCutoverGet(cluster, "/metadata/uid"))
	if clusterUID == "" {
		return errors.New("cannot identify reinitialization cluster")
	}
	if mediaIsolation != nil || purpose == "ota" {
		if _, err := inspect(); err != nil {
			return err
		}
	}
	journal := storageCutoverJournal{Operation: "reinitialize", Reinitialization: &proof, SourceFile: sourceFile, DestinationFile: candidateFile, Environment: cfg.Environment, Purpose: purpose, ClusterUID: clusterUID, ID: fmt.Sprint(time.Now().UTC().UnixNano()), Status: "prepared", Mutations: mutations, SourceSecrets: snapshots}
	if err := saveStorageCutoverJournal(store, journal, false); err != nil {
		return fmt.Errorf("preserve the existing storage journal before reinitialization: %w", err)
	}
	defer func() {
		if result != nil {
			journal.Status = "failed"
			saveErr := saveStorageCutoverJournal(store, journal, true)
			result = errors.Join(fmt.Errorf("%w; reinitialization stopped with its private journal retained; deleted-source data rollback is unavailable", result), saveErr)
		}
	}()
	if err := applyStorageCutover(store, &journal); err != nil {
		return err
	}
	if err := verifyStorageCutover(journal); err != nil {
		return err
	}
	if err := waitMediaStorageConsumers(journal, cfg.Values["CLOUD_STACK_NAME"], source, target, access, secret, inspect); err != nil {
		return err
	}
	if purpose == "ota" {
		if err := lkeRequireReadyOTAServiceEndpoint(selectedEnv); err != nil {
			return err
		}
	}
	if err := verifyStorageCutover(journal); err != nil {
		return err
	}
	if err := verifyStorageReinitializationProfiles(proof, sourceFile, candidateFile); err != nil {
		return err
	}
	if mediaIsolation != nil || purpose == "ota" {
		if _, err := inspect(); err != nil {
			return err
		}
	}
	if err := activateStorageCandidateValues(cfg, candidate, purpose, active); err != nil {
		return err
	}
	journal.Status = "complete"
	if err := saveStorageCutoverJournal(store, journal, true); err != nil {
		return err
	}
	receipt := storageReinitializationReceipt{Operation: "reinitialize", Environment: cfg.Environment, Purpose: purpose, Bucket: target.Bucket, Region: target.Region, Prefix: target.Prefix, CutoverID: journal.ID, CutoverAt: time.Now().UTC(), ProofSHA256: storageReinitializationHash(proof), SourceDataDiscarded: true, DataRollbackAvailable: false, ServiceReady: purpose == "ota"}
	name := "storage-cutover.json"
	if purpose == "ota" {
		name = "storage-cutover-ota.json"
	}
	return writeDeploymentStorageState(cfg.Environment, name, receipt)
}

func verifyStorageReinitializationProfiles(proof storageReinitializationProof, sourceFile, candidateFile string) error {
	for path, expected := range map[string]string{sourceFile: proof.SourceProfileSHA256, candidateFile: proof.DestinationProfileSHA256} {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(body)) != expected {
			return errors.New("storage profile changed during reinitialization; credential promotion is blocked")
		}
	}
	return nil
}

// Dedicated OTA activation changes only its existing Deployment and owned Pods.
// Media consumers may still reference the deleted source while their independent
// reinitialization is pending. Never move those consumers into the OTA bucket.
func storageReinitializationOTAInventory(body []byte, stack string, sourceBucket ...string) ([]byte, error) {
	var inventory struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return nil, errors.New("invalid OTA consumer inventory")
	}
	selected := map[string]bool{}
	found := false
	for _, o := range inventory.Items {
		if o["kind"] == "Deployment" && storageCutoverGet(o, "/metadata/namespace") == stack+"-video-cloud" && storageCutoverGet(o, "/metadata/name") == otaServiceWorkloadName {
			uid := storageCutoverString(storageCutoverGet(o, "/metadata/uid"))
			if uid == "" {
				return nil, errors.New("OTA deployment lacks UID")
			}
			selected[uid] = true
			found = true
		}
	}
	if !found {
		return nil, errors.New("OTA reinitialization requires an existing dedicated OTA Deployment; it does not create services or choose images")
	}
	for changed := true; changed; {
		changed = false
		for _, o := range inventory.Items {
			uid := storageCutoverString(storageCutoverGet(o, "/metadata/uid"))
			if uid != "" && !selected[uid] && selected[storageCutoverControllerUID(o)] {
				selected[uid] = true
				changed = true
			}
		}
	}
	items := []map[string]any{}
	cache := map[string]map[string]any{}
	for _, o := range inventory.Items {
		if selected[storageCutoverString(storageCutoverGet(o, "/metadata/uid"))] {
			items = append(items, o)
			continue
		}
		if len(sourceBucket) == 0 {
			continue
		}
		kind := storageCutoverString(o["kind"])
		if kind == "ReplicaSet" || (kind == "Job" && storageCutoverFinishedJob(o)) {
			continue
		}
		path := storageCutoverPodPath(kind)
		if kind == "Pod" {
			if phase := storageCutoverGet(o, "/status/phase"); phase == "Succeeded" || phase == "Failed" {
				continue
			}
			path = "/spec"
		}
		ns := storageCutoverString(storageCutoverGet(o, "/metadata/namespace"))
		for _, containerType := range []string{"containers", "initContainers", "ephemeralContainers"} {
			containers, _ := storageCutoverGet(o, path+"/"+containerType).([]any)
			for _, v := range containers {
				container := storageCutoverMap(v)
				effective, _, err := storageCutoverEffectiveStorageEnv(container, ns, cache)
				if err != nil {
					return nil, err
				}
				if effective["VIDEO_CLOUD_OTA_BLOB_BUCKET"] == sourceBucket[0] || (effective["VIDEO_CLOUD_BLOB_BUCKET"] == sourceBucket[0] && (storageCutoverGet(o, "/metadata/name") == otaServiceWorkloadName || container["name"] == "otaservice")) {
					return nil, fmt.Errorf("additional source-bound OTA consumer %s/%s requires an explicit owner mapping", ns, storageCutoverGet(o, "/metadata/name"))
				}
			}
		}
	}
	return json.Marshal(map[string]any{"items": items})
}

func (c deploymentCredentialChecker) verifyReinitializationEmptyEndpoints(token string, body []byte, source provisionObjectStore) ([]string, error) {
	var inventory struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return nil, errors.New("invalid storage consumer inventory")
	}
	type consumer struct{ name, access string }
	var candidates []consumer
	cache := map[string]map[string]any{}
	for _, object := range inventory.Items {
		kind := storageCutoverString(object["kind"])
		if kind == "ReplicaSet" || (kind == "Job" && storageCutoverFinishedJob(object)) {
			continue
		}
		path := storageCutoverPodPath(kind)
		if kind == "Pod" {
			if phase := storageCutoverGet(object, "/status/phase"); phase == "Succeeded" || phase == "Failed" {
				continue
			}
			path = "/spec"
		}
		namespace := storageCutoverString(storageCutoverGet(object, "/metadata/namespace"))
		name := kind + "/" + namespace + "/" + storageCutoverString(storageCutoverGet(object, "/metadata/name"))
		for _, containerType := range []string{"containers", "initContainers", "ephemeralContainers"} {
			containers, _ := storageCutoverGet(object, path+"/"+containerType).([]any)
			for _, value := range containers {
				container := storageCutoverMap(value)
				effective, indirect, err := storageCutoverEffectiveStorageEnv(container, namespace, cache)
				if err != nil {
					return nil, err
				}
				for _, prefix := range []string{"VIDEO_CLOUD_BLOB_", "VIDEO_CLOUD_OTA_BLOB_", "LINODE_OBJ_"} {
					if effective[prefix+"BUCKET"] != source.bucket || effective[prefix+"ENDPOINT"] != "" {
						continue
					}
					if indirect[prefix+"ENDPOINT"] || indirect[prefix+"BUCKET"] || indirect[prefix+"REGION"] || indirect[prefix+"PREFIX"] || effective[prefix+"REGION"] != source.region || effective[prefix+"PREFIX"] != source.prefix {
						return nil, fmt.Errorf("%s empty endpoint lacks an unambiguous literal source identity", name)
					}
					access := ""
					env, _ := container["env"].([]any)
					for _, v := range env {
						e := storageCutoverMap(v)
						if e["name"] != "AWS_ACCESS_KEY_ID" {
							continue
						}
						access = storageCutoverString(e["value"])
						if from := storageCutoverMap(e["valueFrom"]); from != nil {
							ref := storageCutoverMap(from["secretKeyRef"])
							if ref == nil {
								return nil, errors.New("empty endpoint source key requires a literal or exact Secret reference")
							}
							s, err := storageCutoverRead("Secret", namespace, storageCutoverString(ref["name"]))
							if err != nil {
								return nil, err
							}
							decoded, err := base64.StdEncoding.DecodeString(storageCutoverString(storageCutoverMap(s["data"])[storageCutoverString(ref["key"])]))
							if err != nil {
								return nil, errors.New("source access key Secret has invalid encoding")
							}
							access = string(decoded)
						}
					}
					if access == "" {
						return nil, fmt.Errorf("%s empty endpoint has no verifiable source access-key binding", name)
					}
					candidates = append(candidates, consumer{name + "/" + storageCutoverString(container["name"]), access})
				}
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	var keys []linodeStorageKey
	if err := c.storageAccountInventory(token, "keys", &keys); err != nil {
		return nil, err
	}
	var names []string
	for _, candidate := range candidates {
		verified := false
		for _, key := range keys {
			if key.AccessKey != candidate.access || len(key.BucketAccess) != 1 {
				continue
			}
			grant := key.BucketAccess[0]
			verified = grant.BucketName == source.bucket && grant.Region == source.region && (grant.Permissions == "read_write" || grant.Permissions == "read_only")
		}
		if !verified {
			return nil, fmt.Errorf("%s empty endpoint credential is not scoped to the exact deleted source bucket and region", candidate.name)
		}
		names = append(names, candidate.name)
	}
	return names, nil
}
