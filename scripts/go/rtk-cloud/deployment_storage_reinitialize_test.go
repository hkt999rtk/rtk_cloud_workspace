package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReinitializeRequiresCompleteEmptyDestination(t *testing.T) {
	for _, tc := range []struct{ name, query, body string }{
		{"current", "list-type", `<ListBucketResult><Name>target</Name><IsTruncated>false</IsTruncated><Contents><Key>unrelated</Key></Contents></ListBucketResult>`},
		{"version", "versions", `<ListVersionsResult><Name>target</Name><IsTruncated>false</IsTruncated><Version><VersionId>null</VersionId></Version></ListVersionsResult>`},
		{"delete marker", "versions", `<ListVersionsResult><Name>target</Name><IsTruncated>false</IsTruncated><DeleteMarker/></ListVersionsResult>`},
		{"multipart", "uploads", `<ListMultipartUploadsResult><Bucket>target</Bucket><IsTruncated>false</IsTruncated><Upload/></ListMultipartUploadsResult>`},
		{"incomplete", "versions", `<ListVersionsResult><Name>target</Name><IsTruncated>true</IsTruncated></ListVersionsResult>`},
		{"wrong bucket", "list-type", `<ListBucketResult><Name>different</Name><IsTruncated>false</IsTruncated></ListBucketResult>`},
		{"wrong version bucket", "versions", `<ListVersionsResult><Name>different</Name><IsTruncated>false</IsTruncated></ListVersionsResult>`},
		{"empty version name with version", "versions", `<ListVersionsResult><Name></Name><IsTruncated>false</IsTruncated><Version/></ListVersionsResult>`},
		{"empty version name with delete marker", "versions", `<ListVersionsResult><Name></Name><IsTruncated>false</IsTruncated><DeleteMarker/></ListVersionsResult>`},
		{"empty version name truncated", "versions", `<ListVersionsResult><Name></Name><IsTruncated>true</IsTruncated></ListVersionsResult>`},
		{"empty version name missing termination proof", "versions", `<ListVersionsResult><Name></Name></ListVersionsResult>`},
		{"wrong response", "versions", `<Error/>`},
		{"bare objects", "list-type", `<ListBucketResult/>`},
		{"bare versions", "versions", `<ListVersionsResult/>`},
		{"bare uploads", "uploads", `<ListMultipartUploadsResult/>`},
		{"missing object name", "list-type", `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`},
		{"missing version name", "versions", `<ListVersionsResult><IsTruncated>false</IsTruncated></ListVersionsResult>`},
		{"missing upload bucket", "uploads", `<ListMultipartUploadsResult><IsTruncated>false</IsTruncated></ListMultipartUploadsResult>`},
		{"missing termination proof", "versions", `<ListVersionsResult><Name>target</Name></ListVersionsResult>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("non-read request %s", r.Method)
				}
				if r.URL.Query().Has(tc.query) {
					fmt.Fprint(w, tc.body)
					return
				}
				serveReinitializeEmptyInventory(w, r, "target")
			}))
			defer s.Close()
			c := deploymentCredentialChecker{client: s.Client()}
			if err := c.requireEmptyStorageDestination(provisionObjectStore{bucket: "target", endpoint: s.URL, region: "us-sea"}); err == nil {
				t.Fatal("nonempty/incomplete destination accepted")
			}
		})
	}
}

func TestReinitializeEmptyVersionNameRequiresOtherBucketIdentities(t *testing.T) {
	for _, tc := range []struct {
		name, objects, uploads string
		valid                  bool
	}{
		{"Linode E3 empty versions", "target", "target", true},
		{"wrong objects bucket", "different", "target", false},
		{"empty objects name", "", "target", false},
		{"wrong uploads bucket", "target", "different", false},
		{"empty uploads bucket", "target", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/target" {
					t.Errorf("inventory left the exact read-only destination: %s %s", r.Method, r.URL.Path)
				}
				reads++
				switch {
				case r.URL.Query().Has("versions"):
					fmt.Fprint(w, `<ListVersionsResult><Name></Name><Prefix></Prefix><IsTruncated>false</IsTruncated><MaxKeys>1000</MaxKeys></ListVersionsResult>`)
				case r.URL.Query().Has("uploads"):
					serveReinitializeEmptyInventory(w, r, tc.uploads)
				default:
					serveReinitializeEmptyInventory(w, r, tc.objects)
				}
			}))
			defer s.Close()
			c := deploymentCredentialChecker{client: s.Client()}
			err := c.requireEmptyStorageDestination(provisionObjectStore{bucket: "target", endpoint: s.URL, region: "us-sea"})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid && reads != 3 {
				t.Fatal("empty version name accepted without all three inventory proofs")
			}
			if err != nil {
				query := "objects"
				if tc.objects == "target" {
					query = "uploads"
				}
				if !strings.Contains(err.Error(), "destination "+query+" inventory") {
					t.Fatalf("inventory error omitted operation: %v", err)
				}
			}
		})
	}
}

func serveReinitializeEmptyInventory(w http.ResponseWriter, r *http.Request, bucket string) bool {
	for query, root := range map[string]string{"list-type": "ListBucketResult", "versions": "ListVersionsResult", "uploads": "ListMultipartUploadsResult"} {
		if !r.URL.Query().Has(query) {
			continue
		}
		identity := "Name"
		if query == "uploads" {
			identity = "Bucket"
		}
		fmt.Fprintf(w, "<%s><%s>%s</%s><IsTruncated>false</IsTruncated></%s>", root, identity, bucket, identity, root)
		return true
	}
	return false
}

func TestReinitializeSourceIdentityWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.env")
	good := "RTK_STORAGE_SOURCE_ENVIRONMENT=dev\nLINODE_OBJ_BUCKET=deleted\nLINODE_OBJ_REGION=us-sea\nLINODE_OBJ_ENDPOINT=https://us-sea-1.linodeobjects.com\nLINODE_OBJ_PREFIX=environments/video-cloud-dev\n"
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"identity only", good, true}, {"wrong environment", strings.Replace(good, "ENVIRONMENT=dev", "ENVIRONMENT=staging", 1), false},
		{"missing explicit prefix", strings.Replace(good, "LINODE_OBJ_PREFIX=environments/video-cloud-dev\n", "", 1), false},
		{"traversal", strings.Replace(good, "environments/video-cloud-dev", "environments/../dev", 1), false},
		{"double slash", strings.Replace(good, "environments/video-cloud-dev", "environments//dev", 1), false},
		{"malformed endpoint", strings.Replace(good, "https://us-sea-1.linodeobjects.com", "%invalid", 1), false},
		{"bucket root explicit", strings.Replace(good, "environments/video-cloud-dev", "", 1), true},
		{"legacy opaque segment", strings.Replace(good, "environments/video-cloud-dev", "legacy/DeviceABC", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err := readStorageReinitializationSource(path, "dev")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

type reinitializeFixture struct {
	cfg                         deploymentConfig
	checker                     deploymentCredentialChecker
	store                       secretStore
	source, candidate, mockRoot string
	canary                      map[string][]byte
	failCanary                  bool
	sourcePresent               bool
	providerWrites              int
}

func newReinitializeFixture(t *testing.T) *reinitializeFixture {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	mock := installStorageCutoverMock(t)
	for name, value := range map[string]string{"operator/env/LINODE_TOKEN": "selected-token", "operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "active-old", "operator/env/LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "active-secret", "kube/kubeconfig.yaml": "mock"} {
		if err := store.write(name, []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	f := &reinitializeFixture{store: store, mockRoot: mock, source: filepath.Join(root, "source.env"), candidate: filepath.Join(root, "candidate.env"), canary: map[string][]byte{}}
	f.cfg = deploymentConfig{Environment: "dev", RuntimeRoot: filepath.Join(root, "runtime"), Values: map[string]string{"CLOUD_STACK_NAME": "stack"}, Storage: deploymentStoragePlan{RuntimeMediaCutoverRequired: true, RuntimeMedia: deploymentStorageTarget{Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea", Prefix: "environments/stack"}}}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/object-storage/") {
			if r.Method != "GET" {
				f.providerWrites++
				t.Errorf("unexpected provider mutation")
			}
			if r.URL.Path == "/object-storage/keys" {
				fmt.Fprintf(w, `{"data":[{"id":42,"access_key":"candidate-access","bucket_access":[{"bucket_name":%q,"region":"us-sea","permissions":"read_write"}]},{"id":41,"access_key":"old","bucket_access":[{"bucket_name":"old","region":"us-sea","permissions":"read_write"}]}],"pages":1}`, f.cfg.Storage.RuntimeMedia.Bucket)
				return
			}
			buckets := []map[string]any{{"label": f.cfg.Storage.RuntimeMedia.Bucket, "region": "us-sea", "s3_endpoint": server.URL, "endpoint_type": "E1"}}
			if f.sourcePresent {
				buckets = append(buckets, map[string]any{"label": "old", "region": "us-sea"})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": buckets, "pages": 1})
			return
		}
		if r.URL.Path != "/"+f.cfg.Storage.RuntimeMedia.Bucket {
			switch r.Method {
			case "PUT":
				if f.failCanary {
					http.Error(w, "denied", 403)
					return
				}
				body := make([]byte, r.ContentLength)
				r.Body.Read(body)
				f.canary[r.URL.Path] = body
			case "GET":
				w.Write(f.canary[r.URL.Path])
			case "DELETE":
				delete(f.canary, r.URL.Path)
			default:
				t.Errorf("unexpected method %s", r.Method)
			}
			return
		}
		if serveReinitializeEmptyInventory(w, r, f.cfg.Storage.RuntimeMedia.Bucket) || serveMigrationBucketInspection(w, r) {
			return
		}
		t.Errorf("unexpected storage inspection %s", r.URL.Query().Encode())
	}))
	t.Cleanup(server.Close)
	f.checker = deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL}
	if err := os.WriteFile(f.source, []byte("RTK_STORAGE_SOURCE_ENVIRONMENT=dev\nLINODE_OBJ_BUCKET=old\nLINODE_OBJ_REGION=us-sea\nLINODE_OBJ_ENDPOINT=https://source.example\nLINODE_OBJ_PREFIX=environments/stack\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.candidate, []byte("RTK_STORAGE_CANDIDATE_ENVIRONMENT=dev\nLINODE_MEDIA_OBJ_ACCESS_KEY_ID=candidate-access\nLINODE_MEDIA_OBJ_SECRET_ACCESS_KEY=candidate-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workload := storageCutoverFixture("Deployment", "api")
	containers, _ := storageCutoverGet(workload, "/spec/template/spec/containers").([]any)
	storageCutoverMap(containers[0])["image"] = "existing-image@sha256:unchanged"
	storageCutoverMockWrite(t, mock, workload)
	return f
}

func TestReinitializePlanAndActivation(t *testing.T) {
	f := newReinitializeFixture(t)
	out := captureStdout(t, func() {
		if err := f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, "old", true); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, `"plan": true`) || strings.Contains(out, "candidate-secret") || strings.Contains(out, "candidate-access") {
		t.Fatalf("unsafe or missing plan: %s", out)
	}
	if _, err := f.store.read(storageCutoverJournalName("media")); !os.IsNotExist(err) {
		t.Fatal("plan created a journal")
	}
	commands, _ := os.ReadFile(filepath.Join(f.mockRoot, "commands"))
	if strings.Contains(string(commands), " patch ") || strings.Contains(string(commands), "create ") {
		t.Fatal("plan changed Kubernetes")
	}
	if err := f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, "old", false); err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentStorageActivation(f.cfg); err != nil {
		t.Fatal(err)
	}
	if len(f.canary) != 0 || f.providerWrites != 0 {
		t.Fatal("canary retained or provider mutation")
	}
	raw, err := f.store.read(storageCutoverJournalName("media"))
	if err != nil {
		t.Fatal(err)
	}
	var journal storageCutoverJournal
	json.Unmarshal([]byte(raw), &journal)
	if journal.Operation != "reinitialize" || journal.Status != "complete" || journal.MigrationSHA256 != "" || journal.Reinitialization == nil {
		t.Fatal("journal misrepresents reinitialization")
	}
	if err := validateStorageRollbackData(f.cfg, journal); err == nil {
		t.Fatal("deleted-source rollback accepted")
	}
	live, err := storageCutoverRead("Deployment", "stack-video-cloud", "api")
	if err != nil {
		t.Fatal(err)
	}
	if storageCutoverGet(live, "/spec/template/spec/containers/0/image") != "existing-image@sha256:unchanged" {
		t.Fatal("deployed image changed")
	}
	journal.Reinitialization.Destination.Bucket = "other"
	if err := saveStorageCutoverJournal(f.store, journal, true); err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentStorageActivation(f.cfg); err == nil {
		t.Fatal("changed private proof accepted")
	}
}

func TestReinitializeRejectsPublicReceiptAuthorityBeforeMutation(t *testing.T) {
	f := newReinitializeFixture(t)
	path := mustStorageStatePath(t, f.cfg, "storage-cutover.json")
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, "old", false); err == nil {
		t.Fatal("unprivate receipt authority accepted")
	}
	if _, err := os.Stat(filepath.Join(f.mockRoot, "commands")); !os.IsNotExist(err) {
		t.Fatal("Kubernetes was touched before authority validation")
	}
	if f.providerWrites != 0 || len(f.canary) != 0 {
		t.Fatal("provider or storage canary changed before authority validation")
	}
	if _, err := f.store.read(storageCutoverJournalName("media")); !os.IsNotExist(err) {
		t.Fatal("failed authority check created a journal")
	}
}

func TestReinitializeFailsBeforeWorkloadMutation(t *testing.T) {
	for _, kind := range []string{"source exists", "canary fails", "ack differs"} {
		t.Run(kind, func(t *testing.T) {
			f := newReinitializeFixture(t)
			f.sourcePresent = kind == "source exists"
			f.failCanary = kind == "canary fails"
			ack := "old"
			if kind == "ack differs" {
				ack = "other"
			}
			if err := f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, ack, false); err == nil {
				t.Fatal("unsafe activation passed")
			}
			commands, _ := os.ReadFile(filepath.Join(f.mockRoot, "commands"))
			if strings.Contains(string(commands), "patch ") || strings.Contains(string(commands), "create ") {
				t.Fatal("failed precondition changed Kubernetes")
			}
			if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover.json")); !os.IsNotExist(err) {
				t.Fatal("failed activation emitted receipt")
			}
		})
	}
}

func TestReinitializeEmptyEndpointNeedsExactSourceCredential(t *testing.T) {
	f := newReinitializeFixture(t)
	o := storageCutoverFixture("Deployment", "api")
	env, _ := storageCutoverGet(o, "/spec/template/spec/containers/0/env").([]any)
	storageCutoverMap(env[1])["value"] = ""
	body, _ := json.Marshal(map[string]any{"items": []any{o}})
	source := provisionObjectStore{bucket: "old", region: "us-sea", endpoint: "https://source.example", prefix: "environments/stack", prefixSet: true}
	names, err := f.checker.verifyReinitializationEmptyEndpoints("selected-token", body, source)
	if err != nil || len(names) != 1 {
		t.Fatalf("exact empty-endpoint source rejected: %v", err)
	}
	source.reinitializeEmptyEndpoint = true
	target := f.cfg.Storage.RuntimeMedia
	target.Endpoint = "https://destination.example"
	if _, _, err := planStorageConsumers(body, "stack", source, target, "candidate-access", "candidate-secret", "media"); err != nil {
		t.Fatal(err)
	}
	source.reinitializeEmptyEndpoint = false
	if _, _, err := planMediaStorageConsumers(body, "stack", source, target, "candidate-access", "candidate-secret"); err == nil {
		t.Fatal("ordinary migration accepted empty endpoint")
	}
	storageCutoverMap(env[4])["valueFrom"] = nil
	storageCutoverMap(env[4])["value"] = "foreign-access"
	body, _ = json.Marshal(map[string]any{"items": []any{o}})
	if _, err := f.checker.verifyReinitializationEmptyEndpoints("selected-token", body, source); err == nil {
		t.Fatal("unrelated credential accepted as source proof")
	}
}

func TestReinitializePromotionUsesVerifiedSnapshotAndActiveCAS(t *testing.T) {
	f := newReinitializeFixture(t)
	values, check := deploymentCredentialValuesFromFile(f.candidate)
	if !check.Passed {
		t.Fatal(check.Detail)
	}
	before, err := f.store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	oldSource, _ := os.ReadFile(f.source)
	oldCandidate, _ := os.ReadFile(f.candidate)
	proof := storageReinitializationProof{SourceProfileSHA256: fmt.Sprintf("%x", sha256.Sum256(oldSource)), DestinationProfileSHA256: fmt.Sprintf("%x", sha256.Sum256(oldCandidate))}
	os.WriteFile(f.candidate, []byte(strings.ReplaceAll(string(oldCandidate), "candidate-access", "changed-access")), 0600)
	if err := verifyStorageReinitializationProfiles(proof, f.source, f.candidate); err == nil {
		t.Fatal("candidate replacement during rollout was ignored")
	}
	if err := f.store.write("operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID", []byte("concurrent-active-key"), true); err != nil {
		t.Fatal(err)
	}
	if err := activateStorageCandidateValues(f.cfg, values, "media", before); err == nil {
		t.Fatal("concurrent active credential was overwritten")
	}
}

func TestReinitializeOTAInventoryKeepsMediaSeparate(t *testing.T) {
	ota := storageCutoverFixture("Deployment", otaServiceWorkloadName)
	media := storageCutoverFixture("Deployment", "api")
	body, _ := json.Marshal(map[string]any{"items": []any{ota, media}})
	filtered, err := storageReinitializationOTAInventory(body, "stack")
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Items []map[string]any }
	json.Unmarshal(filtered, &result)
	if len(result.Items) != 1 || storageCutoverGet(result.Items[0], "/metadata/name") != otaServiceWorkloadName {
		t.Fatal("OTA reinitialization selected media consumers")
	}
	foreign := storageCutoverFixture("Deployment", otaServiceWorkloadName)
	metadata := storageCutoverMap(foreign["metadata"])
	metadata["namespace"], metadata["uid"] = "another-video-cloud", "foreign-ota"
	body, _ = json.Marshal(map[string]any{"items": []any{ota, media, foreign}})
	if _, err := storageReinitializationOTAInventory(body, "stack", "old"); err == nil {
		t.Fatal("additional source-bound OTA consumer was silently excluded")
	}
}

func TestReinitializeRejectsCandidateReplacementDuringRollout(t *testing.T) {
	f := newReinitializeFixture(t)
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	replacement := filepath.Join(t.TempDir(), "replacement.env")
	body, err := os.ReadFile(f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(string(body), "candidate-access", "changed-after-rollout")), 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "kubectl")
	script := "#!/bin/sh\ncase \" $* \" in *\" rollout \"*) cp \"$REINITIALIZE_TEST_REPLACEMENT\" \"$REINITIALIZE_TEST_CANDIDATE\" ;; esac\nexec \"$REINITIALIZE_TEST_KUBECTL\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REINITIALIZE_TEST_REPLACEMENT", replacement)
	t.Setenv("REINITIALIZE_TEST_CANDIDATE", f.candidate)
	t.Setenv("REINITIALIZE_TEST_KUBECTL", original)
	t.Setenv("RTK_CLOUD_KUBECTL", wrapper)
	err = f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, "old", false)
	if err == nil || !strings.Contains(err.Error(), "profile changed during reinitialization") {
		t.Fatalf("candidate changed during rollout was accepted: %v", err)
	}
	active, err := f.store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	if active["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "active-old" {
		t.Fatal("unverified candidate was promoted")
	}
	if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover.json")); !os.IsNotExist(err) {
		t.Fatal("failed reinitialization left an activation receipt")
	}
	raw, err := f.store.read(storageCutoverJournalName("media"))
	if err != nil {
		t.Fatal(err)
	}
	var journal storageCutoverJournal
	if json.Unmarshal([]byte(raw), &journal) != nil || journal.Status != "failed" {
		t.Fatal("incomplete operation did not retain its failure journal")
	}
}
