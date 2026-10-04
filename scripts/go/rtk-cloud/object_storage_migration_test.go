package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type migrationTestStorage struct {
	objects map[string]storageObjectSnapshot
	puts    int
	race    *storageObjectSnapshot
}

func (s *migrationTestStorage) serve(w http.ResponseWriter, r *http.Request) {
	if serveMigrationBucketInspection(w, r) {
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/")
	if r.URL.Query().Get("list-type") == "2" {
		result := provisionListBucketResult{}
		for name, object := range s.objects {
			if relative, ok := strings.CutPrefix(name, key+"/"); ok && strings.HasPrefix(relative, r.URL.Query().Get("prefix")) {
				result.Contents = append(result.Contents, provisionObjectEntry{Key: relative, Size: int64(len(object.Body))})
			}
		}
		_ = xml.NewEncoder(w).Encode(result)
		return
	}
	object, found := s.objects[key]
	switch r.Method {
	case http.MethodGet:
		if !found {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Has("tagging") {
			tags, _ := url.ParseQuery(object.Tags)
			var document struct {
				XMLName xml.Name `xml:"Tagging"`
				Tags    []struct {
					Key   string `xml:"Key"`
					Value string `xml:"Value"`
				} `xml:"TagSet>Tag"`
			}
			for name, values := range tags {
				for _, value := range values {
					document.Tags = append(document.Tags, struct {
						Key   string `xml:"Key"`
						Value string `xml:"Value"`
					}{name, value})
				}
			}
			_ = xml.NewEncoder(w).Encode(document)
			return
		}
		for name, values := range object.Headers {
			w.Header()[name] = values
		}
		_, _ = w.Write(object.Body)
	case http.MethodPut:
		s.puts++
		if r.Header.Get("If-None-Match") != "*" {
			http.Error(w, "conditional write required", 400)
			return
		}
		if s.race != nil {
			s.objects[key] = *s.race
			s.race = nil
			found = true
		}
		if found {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		headers := http.Header{}
		for name, values := range r.Header {
			lower := strings.ToLower(name)
			if strings.HasPrefix(lower, "x-amz-meta-") || strings.HasPrefix(lower, "x-amz-checksum-") || keySet("content-type", "cache-control", "content-disposition", "content-encoding", "content-language", "expires")[lower] {
				headers[name] = values
			}
		}
		s.objects[key] = storageObjectSnapshot{Body: body, Headers: headers, Tags: r.Header.Get("X-Amz-Tagging"), MetadataKnown: true}
	default:
		http.Error(w, "unexpected method", 405)
	}
}

func migrationTestObject(body string) storageObjectSnapshot {
	sum := sha256.Sum256([]byte(body))
	return storageObjectSnapshot{Body: []byte(body), Headers: http.Header{"Content-Type": {"video/mp4"}, "X-Amz-Checksum-Sha256": {base64.StdEncoding.EncodeToString(sum[:])}, "Cache-Control": {"private, max-age=60"}, "X-Amz-Meta-Device-Id": {"camera-1"}, "X-Amz-Meta-Sha256": {base64.StdEncoding.EncodeToString([]byte("verified-checksum"))}}, Tags: "owner=video&retention=7%20days", MetadataKnown: true}
}

func TestStorageCopyPreservesMetadataTagsAndConditionalRetry(t *testing.T) {
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	destination := provisionObjectStore{bucket: "destination", endpoint: server.URL, region: "test", accessKey: "access", secretKey: "secret"}
	key := "clips/a space.mp4"
	object := migrationTestObject("clip bytes")
	if err := copyStorageObject(server.Client(), object, destination, key); err != nil {
		t.Fatal(err)
	}
	if err := copyStorageObject(server.Client(), object, destination, key); err != nil {
		t.Fatal(err)
	}
	if fixture.puts != 1 {
		t.Fatalf("retry overwrote object: puts=%d", fixture.puts)
	}
	got, err := readStorageObject(server.Client(), destination, key)
	if err != nil || got.proof() != object.proof() {
		t.Fatalf("metadata round trip = %+v, %v", got.proof(), err)
	}
	conflict := object
	conflict.Headers = object.Headers.Clone()
	conflict.Headers.Set("Content-Type", "application/octet-stream")
	if err := copyStorageObject(server.Client(), conflict, destination, key); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("metadata conflict accepted: %v", err)
	}
	if fixture.puts != 1 {
		t.Fatal("conflict overwrote object")
	}
}

func TestStorageCopyConditionalWriteCollisionCannotOverwrite(t *testing.T) {
	raced := migrationTestObject("concurrent writer")
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{}, race: &raced}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	destination := provisionObjectStore{bucket: "destination", endpoint: server.URL, region: "test", accessKey: "access", secretKey: "secret"}
	if err := copyStorageObject(server.Client(), migrationTestObject("intended"), destination, "clips/collision"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("concurrent collision accepted: %v", err)
	}
	if !bytes.Equal(fixture.objects["destination/clips/collision"].Body, raced.Body) {
		t.Fatal("concurrent object overwritten")
	}
}

func TestStorageMigrationBindsPrefixMappingAndRechecksResume(t *testing.T) {
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{"source/old/clips/clip.mp4": migrationTestObject("bytes"), "source/clips/ignored.mp4": migrationTestObject("outside explicit prefix")}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	source := provisionObjectStore{bucket: "source", endpoint: server.URL, region: "source-region", prefix: "old", prefixSet: true, accessKey: "access", secretKey: "secret"}
	destination := provisionObjectStore{bucket: "destination", endpoint: server.URL, region: "destination-region", accessKey: "access", secretKey: "secret"}
	checker := deploymentCredentialChecker{client: server.Client()}
	statePath := filepath.Join(t.TempDir(), "migration.json")
	run := func() error {
		return checker.migrateStorageObjects("dev", source, destination, "new", statePath, "media", []string{"clips/"})
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("exact retry failed: %v", err)
	}
	if fixture.puts != 1 {
		t.Fatal("resume performed extra writes")
	}
	var state deploymentStorageMigrationState
	body, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if state.ObjectCount != 1 || state.SourceKeys["old/clips/clip.mp4"] != "new/clips/clip.mp4" {
		t.Fatalf("receipt mapping = %+v", state)
	}
	source.region = "another-region"
	if err := run(); err == nil || !strings.Contains(err.Error(), "different storage") {
		t.Fatalf("source identity change accepted: %v", err)
	}
	source.region = "source-region"
	fixture.objects["source/old/clips/clip.mp4"] = migrationTestObject("source changed")
	if err := run(); err == nil || !strings.Contains(err.Error(), "differs from migration receipt") {
		t.Fatalf("changed source accepted: %v", err)
	}
	fixture.objects["source/old/clips/clip.mp4"] = migrationTestObject("bytes")
	fixture.objects["destination/new/clips/clip.mp4"] = migrationTestObject("destination changed")
	if err := run(); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("changed destination accepted: %v", err)
	}
	delete(fixture.objects, "source/old/clips/clip.mp4")
	if err := run(); err == nil || !strings.Contains(err.Error(), "source mapping") {
		t.Fatalf("removed source accepted: %v", err)
	}
}

func TestStorageMigrationRejectsMappingCollisionsBeforeWrites(t *testing.T) {
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{"source/clips/a": migrationTestObject("a"), "source/new/clips/a": migrationTestObject("b")}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	source := provisionObjectStore{bucket: "source", endpoint: server.URL, region: "test"}
	_, err := listStorageSourceObjects(source, "new", []string{"clips/"})
	if err == nil || !strings.Contains(err.Error(), "same destination") {
		t.Fatalf("collision accepted: %v", err)
	}
	if fixture.puts != 0 {
		t.Fatal("collision wrote objects")
	}
}

func TestStorageCopyRejectsCompositeChecksumWithoutDroppingIt(t *testing.T) {
	object := migrationTestObject("bytes")
	object.Headers.Set("X-Amz-Checksum-Type", "COMPOSITE")
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{"source/clips/a": object}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	_, err := readStorageObject(server.Client(), provisionObjectStore{bucket: "source", endpoint: server.URL, region: "test"}, "clips/a")
	if err == nil || !strings.Contains(err.Error(), "multipart composite") {
		t.Fatalf("unsupported checksum silently changed: %v", err)
	}
}

func TestStorageRequestSignsMetadataAndEscapesKeyOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/bucket/space%20name" {
			t.Errorf("request path=%s", r.URL.EscapedPath())
		}
		for _, header := range []string{"content-type", "if-none-match", "x-amz-tagging"} {
			if !strings.Contains(r.Header.Get("Authorization"), header) {
				t.Errorf("unsigned header %s", header)
			}
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	_, _, err := provisionSignedObjectRequestHeaders(server.Client(), provisionObjectStore{bucket: "bucket", endpoint: server.URL, region: "test"}, http.MethodPut, "space name", nil, []byte("data"), http.Header{"Content-Type": {"video/mp4"}, "If-None-Match": {"*"}, "X-Amz-Tagging": {"owner=video"}})
	if err != nil {
		t.Fatal(err)
	}
}

func serveMigrationBucketInspection(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.URL.Query().Has("acl") {
		fmt.Fprint(w, privateStorageACLForTest)
		return true
	}
	if r.URL.Query().Has("policy") {
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	for query, root := range map[string]string{"versioning": "VersioningConfiguration", "versions": "ListVersionsResult", "uploads": "ListMultipartUploadsResult"} {
		if r.URL.Query().Has(query) {
			fmt.Fprintf(w, "<%s/>", root)
			return true
		}
	}
	return false
}

func TestStorageMigrationRejectsIncompleteBucketHistory(t *testing.T) {
	for _, tc := range []struct{ name, query, body string }{
		{"versioning", "versioning", `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`},
		{"suspended", "versioning", `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`},
		{"history", "versions", `<ListVersionsResult><Version><VersionId>old-version</VersionId></Version></ListVersionsResult>`},
		{"delete-marker", "versions", `<ListVersionsResult><DeleteMarker/></ListVersionsResult>`},
		{"incomplete-list", "versions", `<ListVersionsResult><IsTruncated>true</IsTruncated></ListVersionsResult>`},
		{"multipart", "uploads", `<ListMultipartUploadsResult><Upload/></ListMultipartUploadsResult>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has(tc.query) {
					fmt.Fprint(w, tc.body)
					return
				}
				if !serveMigrationBucketInspection(w, r) {
					t.Errorf("unexpected request %s", r.URL)
				}
			}))
			defer server.Close()
			if err := validateStorageCopyBucket(server.Client(), provisionObjectStore{bucket: "test", endpoint: server.URL, region: "test"}); err == nil {
				t.Fatal("incomplete migration accepted")
			}
		})
	}
}

func TestArtifactMigrationPreservesAllRootNamespaces(t *testing.T) {
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{}}
	keys := []string{"releases/video/v1.tar.gz", "ci/run123/output.zip", "sdk/current.json", "backups/core/backup.age", "/releases/video/v1.tar.gz", "//releases/video/v1.tar.gz", "ci//literal/../percent%name", "sdk/folder-marker/"}
	for _, key := range keys {
		fixture.objects["source/"+key] = migrationTestObject(key)
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	source := provisionObjectStore{bucket: "source", endpoint: server.URL, region: "test", prefixSet: true}
	destination := provisionObjectStore{bucket: "destination", endpoint: server.URL, region: "test"}
	checker := deploymentCredentialChecker{client: server.Client()}
	statePath := filepath.Join(t.TempDir(), "artifacts.json")
	if err := checker.migrateStorageObjects("shared", source, destination, "", statePath, "release-artifacts", []string{""}); err != nil {
		t.Fatal(err)
	}
	if err := checker.validateStorageMigrationCutover("shared", source, destination, "", statePath, "release-artifacts", []string{""}); err != nil {
		t.Fatal(err)
	}
	if fixture.puts != len(keys) {
		t.Fatalf("copied %d of %d keys", fixture.puts, len(keys))
	}
	var receipt deploymentStorageMigrationState
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if _, found := fixture.objects["destination/"+key]; !found {
			t.Errorf("missing preserved key %s", key)
		}
		if receipt.SourceKeys[key] != key {
			t.Errorf("key %q was rewritten as %q", key, receipt.SourceKeys[key])
		}
	}
	if err := checker.migrateStorageObjects("shared", source, destination, "", statePath, "release-artifacts", []string{""}); err != nil {
		t.Fatal(err)
	}
	if fixture.puts != len(keys) {
		t.Fatal("exact artifact retry rewrote objects")
	}
}

func TestStorageCutoverRejectsChangedEvidenceAndObjects(t *testing.T) {
	for _, scenario := range []string{"missing-receipt", "corrupt-receipt", "wrong-purpose", "wrong-destination", "wrong-mapping", "wrong-totals", "source-changed", "source-deleted", "destination-deleted", "destination-extra", "source-read-denied", "source-list-denied", "destination-list-denied", "destination-versioning"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{"source/clips/a": migrationTestObject("source bytes")}}
			fault := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fault == "source-read-denied" && r.URL.Path == "/source/clips/a" || fault == "source-list-denied" && r.URL.Path == "/source" && r.URL.Query().Has("list-type") || fault == "destination-list-denied" && r.URL.Path == "/destination" && r.URL.Query().Has("list-type") {
					http.Error(w, "permission revoked", http.StatusForbidden)
					return
				}
				if fault == "destination-versioning" && r.URL.Path == "/destination" && r.URL.Query().Has("versioning") {
					fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
					return
				}
				fixture.serve(w, r)
			}))
			defer server.Close()
			checker := deploymentCredentialChecker{client: server.Client()}
			source := provisionObjectStore{bucket: "source", endpoint: server.URL, region: "test", prefixSet: true}
			destination := provisionObjectStore{bucket: "destination", endpoint: server.URL, region: "test"}
			statePath := filepath.Join(t.TempDir(), "migration.json")
			if err := checker.migrateStorageObjects("dev", source, destination, "new", statePath, "media", []string{"clips/"}); err != nil {
				t.Fatal(err)
			}
			var receipt deploymentStorageMigrationState
			body, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong-purpose":
				receipt.Purpose = "ota-firmware"
			case "wrong-destination":
				receipt.Destination = "another-bucket"
			case "wrong-mapping":
				receipt.SourceKeys["clips/a"] = "new/clips/other"
			case "wrong-totals":
				receipt.ByteCount++
			case "source-changed":
				fixture.objects["source/clips/a"] = migrationTestObject("changed bytes")
			case "source-deleted":
				delete(fixture.objects, "source/clips/a")
			case "destination-deleted":
				delete(fixture.objects, "destination/new/clips/a")
			case "destination-extra":
				fixture.objects["destination/new/clips/unreviewed"] = migrationTestObject("unreviewed bytes")
			default:
				fault = scenario
			}
			if err := writeStorageState(statePath, receipt); err != nil {
				t.Fatal(err)
			}
			if scenario == "missing-receipt" {
				if err := os.Remove(statePath); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "corrupt-receipt" {
				if err := os.WriteFile(statePath, []byte("partial receipt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := checker.validateStorageMigrationCutover("dev", source, destination, "new", statePath, "media", []string{"clips/"}); err == nil {
				t.Fatal("unsafe cutover accepted")
			}
			if fixture.puts != 1 {
				t.Fatal("cutover verification changed destination objects")
			}
			// Resume must not bless changed receipt identity or removed source mappings.
			if scenario == "corrupt-receipt" || scenario == "wrong-purpose" || scenario == "wrong-destination" || scenario == "wrong-mapping" || scenario == "source-deleted" {
				if err := checker.migrateStorageObjects("dev", source, destination, "new", statePath, "media", []string{"clips/"}); err == nil {
					t.Fatal("unsafe resume accepted")
				}
				if fixture.puts != 1 {
					t.Fatal("unsafe resume wrote objects")
				}
			}
		})
	}
}

func TestStorageMigrationDoesNotRecordSourceChangedDuringCopy(t *testing.T) {
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{"source/clips/a": migrationTestObject("original")}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.serve(w, r)
		if r.Method == http.MethodPut {
			fixture.objects["source/clips/a"] = migrationTestObject("changed while copying")
		}
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client()}
	source := provisionObjectStore{bucket: "source", endpoint: server.URL, region: "test", prefixSet: true}
	destination := provisionObjectStore{bucket: "destination", endpoint: server.URL, region: "test"}
	statePath := filepath.Join(t.TempDir(), "migration.json")
	if err := checker.migrateStorageObjects("dev", source, destination, "new", statePath, "media", []string{"clips/"}); err == nil || !strings.Contains(err.Error(), "changed during migration") {
		t.Fatalf("source mutation accepted: %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("unverified copy recorded as complete: %v", err)
	}
}

type storagePurposeFixture struct {
	objects                   *migrationTestStorage
	bucketExists              bool
	failure                   string
	bucketCreates, keyCreates int
}

func newStoragePurposeFixture(t *testing.T, exists bool, failure string) (deploymentCredentialChecker, deploymentConfig, *storagePurposeFixture) {
	t.Helper()
	f := &storagePurposeFixture{objects: &migrationTestStorage{objects: map[string]storageObjectSnapshot{}}, bucketExists: exists, failure: failure}
	bucketName := "destination"
	if !exists {
		bucketName = "rtk-cloud-shared-artifacts-test"
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bucket := func() string {
			endpoint := server.URL
			if f.failure == "endpoint" {
				endpoint = "ftp://storage.invalid"
			}
			return fmt.Sprintf(`{"label":%q,"region":"test","s3_endpoint":%q,"endpoint_type":"E3"}`, bucketName, endpoint)
		}
		switch r.URL.Path {
		case "/v4/object-storage/buckets":
			if r.Method == http.MethodGet {
				if f.failure == "inventory" {
					http.Error(w, "inventory denied", 403)
					return
				}
				if f.bucketExists {
					fmt.Fprintf(w, `{"data":[%s]}`, bucket())
				} else {
					fmt.Fprint(w, `{"data":[]}`)
				}
				return
			}
			f.bucketCreates++
			if f.failure == "create" {
				http.Error(w, "create rejected", 400)
				return
			}
			if f.failure == "create-json" {
				fmt.Fprint(w, "invalid response")
				return
			}
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["label"] != bucketName || body["region"] != "test" {
				t.Error("bucket creation changed reviewed identity")
			}
			f.bucketExists = true
			fmt.Fprint(w, bucket())
			return
		case "/v4/regions/test":
			if f.failure == "region" {
				http.Error(w, "region unavailable", 400)
				return
			}
			fmt.Fprint(w, `{"id":"test","status":"ok","capabilities":["Kubernetes","Object Storage"]}`)
			return
		case "/v4/object-storage/keys":
			if r.Method == http.MethodGet {
				fmt.Fprintf(w, `{"data":[{"id":7,"access_key":"candidate-access","bucket_access":[{"bucket_name":%q,"region":"test","permissions":"read_write"}]}]}`, bucketName)
				return
			}
			f.keyCreates++
			if f.failure == "key" {
				http.Error(w, "key rejected", 400)
				return
			}
			// Inspect scope without retaining the generated access-key secret.
			raw, _ := io.ReadAll(r.Body)
			if !bytes.Contains(raw, []byte(fmt.Sprintf(`"bucket_name":%q`, bucketName))) || !bytes.Contains(raw, []byte(`"permissions":"read_write"`)) {
				t.Error("key is not limited to the reviewed bucket")
			}
			fmt.Fprint(w, `{"access_key":"candidate-access","secret_key":"candidate-secret"}`)
			return
		}
		if strings.Contains(r.URL.Path, "/__rtk_cloud_validation__/") {
			if f.failure == "canary" {
				http.Error(w, "canary rejected", 400)
				return
			}
			key := strings.TrimPrefix(r.URL.Path, "/")
			switch r.Method {
			case http.MethodPut:
				body, _ := io.ReadAll(r.Body)
				f.objects.objects[key] = storageObjectSnapshot{Body: body}
			case http.MethodGet:
				_, _ = w.Write(f.objects.objects[key].Body)
			case http.MethodDelete:
				delete(f.objects.objects, key)
			default:
				t.Errorf("unexpected canary method %s", r.Method)
			}
			return
		}
		f.objects.serve(w, r)
	}))
	t.Cleanup(server.Close)
	target := deploymentStorageTarget{Bucket: bucketName, Region: "test", Prefix: "new", Endpoint: server.URL}
	cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{RuntimeMedia: target, ReleaseArtifacts: target, OTAFirmware: target, OTAMode: "dedicated"}}
	return deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4", readOnly: true}, cfg, f
}

func TestArtifactBootstrapCreatesOnlyCandidateCredentials(t *testing.T) {
	for _, failure := range []string{"", "inventory", "region", "create", "create-json", "endpoint", "key", "canary", "candidate-unwritable"} {
		t.Run(failure, func(t *testing.T) {
			checker, cfg, fixture := newStoragePurposeFixture(t, false, failure)
			active := filepath.Join(t.TempDir(), "active.env")
			original := []byte("LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID=active-access\nLINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY=active-secret\nLINODE_MEDIA_OBJ_ACCESS_KEY_ID=media-access\n")
			if err := os.WriteFile(active, original, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_TEST_MODE", "1")
			t.Setenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE", active)
			candidate := filepath.Join(t.TempDir(), "candidate.env")
			if err := os.WriteFile(candidate, original, 0600); err != nil {
				t.Fatal(err)
			}
			if failure == "candidate-unwritable" {
				candidate = filepath.Join(active, "candidate.env")
			}
			values := map[string]string{"LINODE_TOKEN": "token", "LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID": "active-access", "LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY": "active-secret"}
			err := checker.bootstrapArtifactStorage(cfg, values, candidate)
			if (err != nil) != (failure != "") {
				t.Fatalf("bootstrap result %v for %q", err, failure)
			}
			after, readErr := os.ReadFile(active)
			if readErr != nil || !bytes.Equal(after, original) || values["LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID"] != "active-access" {
				t.Fatal("preparation changed active credentials")
			}
			if failure != "" {
				return
			}
			prepared, check := deploymentCredentialValuesFromFile(candidate)
			if !check.Passed || prepared["LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID"] != "candidate-access" || prepared["LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY"] != "candidate-secret" || prepared["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "media-access" {
				t.Fatalf("candidate credentials incorrect: %s", check.Detail)
			}
			if fixture.bucketCreates != 1 || fixture.keyCreates != 1 || len(fixture.objects.objects) != 0 {
				t.Fatal("bootstrap did not create one scoped candidate and clean its canary")
			}
			prepared["LINODE_TOKEN"] = "token"
			if err := checker.bootstrapArtifactStorage(cfg, prepared, candidate); err != nil {
				t.Fatal(err)
			}
			if fixture.bucketCreates != 1 || fixture.keyCreates != 1 {
				t.Fatal("prepared candidate retry created duplicate resources")
			}
		})
	}
}

func TestStoragePurposeMigrationValidatesInputsBeforeCopy(t *testing.T) {
	for _, scenario := range []string{"artifacts", "media", "ota", "artifact-partial-prefix", "missing-source-profile", "incomplete-source-profile", "destination-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			checker, cfg, fixture := newStoragePurposeFixture(t, true, "")
			values := map[string]string{"LINODE_TOKEN": "token"}
			for _, purpose := range []string{"ARTIFACT", "MEDIA", "OTA"} {
				values["LINODE_"+purpose+"_OBJ_ACCESS_KEY_ID"], values["LINODE_"+purpose+"_OBJ_SECRET_ACCESS_KEY"] = "candidate-access", "candidate-secret"
			}
			purpose := scenario
			if scenario != "media" && scenario != "ota" {
				purpose = "artifacts"
			}
			key := map[string]string{"artifacts": "releases/v1/archive.tgz", "media": "clips/a", "ota": "ota-billable-v1/brand/product/release/firmware.bin"}[purpose]
			fixture.objects.objects["source/"+key] = migrationTestObject("verified payload")
			sourceFile := filepath.Join(t.TempDir(), "source.env")
			body := fmt.Sprintf("LINODE_OBJ_BUCKET=source\nLINODE_OBJ_ENDPOINT=%s\nLINODE_OBJ_REGION=test\nLINODE_OBJ_ACCESS_KEY_ID=source-access\nLINODE_OBJ_SECRET_ACCESS_KEY=source-secret\nLINODE_OBJ_PREFIX=\n", cfg.Storage.RuntimeMedia.Endpoint)
			if scenario == "artifact-partial-prefix" {
				body = strings.Replace(body, "LINODE_OBJ_PREFIX=\n", "LINODE_OBJ_PREFIX=releases\n", 1)
			}
			if scenario == "incomplete-source-profile" {
				body = "LINODE_OBJ_BUCKET=source\n"
			}
			if scenario != "missing-source-profile" {
				if err := os.WriteFile(sourceFile, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "destination-unavailable" {
				fixture.failure = "inventory"
			}
			err := checker.migrateStoragePurpose(cfg, values, sourceFile, purpose)
			valid := scenario == "artifacts" || scenario == "media" || scenario == "ota"
			if (err == nil) != valid {
				t.Fatalf("migration result %v for %s", err, scenario)
			}
			if !valid {
				if fixture.objects.puts != 0 {
					t.Fatal("invalid inputs copied objects")
				}
				return
			}
			destinationKey := key
			if purpose != "artifacts" {
				destinationKey = "new/" + key
			}
			if _, found := fixture.objects.objects["destination/"+destinationKey]; !found {
				t.Fatal("purpose selected the wrong namespace")
			}
			if purpose == "media" {
				if err := checker.validateMediaMigrationCutover(cfg, values, sourceFile); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMediaMigrationCutoverRequiresUsableSourceAndDestination(t *testing.T) {
	for _, scenario := range []string{"missing-argument", "missing-profile", "invalid-source", "inventory", "endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			checker, cfg, _ := newStoragePurposeFixture(t, true, scenario)
			sourceFile := filepath.Join(t.TempDir(), "source.env")
			if scenario == "missing-argument" {
				sourceFile = ""
			} else if scenario != "missing-profile" {
				body := "LINODE_OBJ_BUCKET=source\nLINODE_OBJ_ENDPOINT=file:///unused\n"
				if scenario == "invalid-source" {
					body = "LINODE_OBJ_REGION=test\n"
				}
				if err := os.WriteFile(sourceFile, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := checker.validateMediaMigrationCutover(cfg, map[string]string{"LINODE_TOKEN": "token"}, sourceFile); err == nil {
				t.Fatal("unverifiable source or destination allowed cutover")
			}
		})
	}
}

func TestStorageCopyPreservesGzipWireBytes(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte("compressed archive content"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	object := migrationTestObject(compressed.String())
	object.Headers.Set("Content-Encoding", "gzip")
	fixture := &migrationTestStorage{objects: map[string]storageObjectSnapshot{"source/clips/archive": object}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	source := provisionObjectStore{bucket: "source", endpoint: server.URL, region: "test"}
	snapshot, err := readStorageObject(server.Client(), source, "clips/archive")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.Body, compressed.Bytes()) {
		t.Fatal("read transparently decoded gzip bytes")
	}
	destination := provisionObjectStore{bucket: "destination", endpoint: server.URL, region: "test"}
	if err := copyStorageObject(server.Client(), snapshot, destination, "clips/archive"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixture.objects["destination/clips/archive"].Body, compressed.Bytes()) {
		t.Fatal("copy changed compressed bytes")
	}
}

func TestStorageHistoryInspectionPaginatesCurrentNullVersions(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("versions") {
			serveMigrationBucketInspection(w, r)
			return
		}
		pages++
		if r.URL.Query().Get("key-marker") == "" {
			fmt.Fprint(w, `<ListVersionsResult><Version><VersionId>null</VersionId></Version><IsTruncated>true</IsTruncated><NextKeyMarker>next key</NextKeyMarker><NextVersionIdMarker>null</NextVersionIdMarker></ListVersionsResult>`)
		} else {
			if !strings.Contains(r.URL.RawQuery, "key-marker=next%20key") {
				t.Errorf("incorrect SigV4 query encoding %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `<ListVersionsResult><Version><VersionId>null</VersionId></Version></ListVersionsResult>`)
		}
	}))
	defer server.Close()
	if err := validateStorageCopyBucket(server.Client(), provisionObjectStore{bucket: "test", endpoint: server.URL, region: "test"}); err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Fatalf("history pages=%d", pages)
	}
}
