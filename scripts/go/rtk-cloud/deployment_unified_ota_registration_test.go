package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func otaRegistrationManifestFixture() *environmentOTARegistrationManifest {
	return &environmentOTARegistrationManifest{ServiceID: "ota", ManifestVersion: "2", ProtocolVersion: "1", EndpointRef: "ota-service", Options: []environmentOTARegistrationOption{{Code: "ota", DisplayName: "Firmware OTA", Description: "Per-Product firmware releases and device updates", Requires: []string{"mqtt"}}}}
}

func otaRegistrationStoredFixture(m *environmentOTARegistrationManifest) map[string]any {
	return map[string]any{"manifest_version": m.ManifestVersion, "digest_sha256": m.digest(), "protocol_version": m.ProtocolVersion, "endpoint_ref": m.EndpointRef, "options": m.Options}
}

func TestOTARegistrationFullHashMatchesAMImmutableV2Contract(t *testing.T) {
	m := otaRegistrationManifestFixture()
	if m.digest() != "b5dff19faa7f24aaf9c602eaf876546dd9cc071cbc670a8a4855e1b19c77e0d6" {
		t.Fatal("canonical digest differs from the AM full immutable manifest projection", m.digest())
	}
	original := m.digest()
	for _, field := range []string{"version", "protocol", "endpoint", "display", "description", "requires"} {
		changed := otaRegistrationManifestFixture()
		switch field {
		case "version":
			changed.ManifestVersion = "3"
		case "protocol":
			changed.ProtocolVersion = "2"
		case "endpoint":
			changed.EndpointRef = "changed-endpoint"
		case "display":
			changed.Options[0].DisplayName = "Changed display"
		case "description":
			changed.Options[0].Description = "Changed description"
		case "requires":
			changed.Options[0].Requires = nil
		}
		if changed.digest() == original {
			t.Fatal("full immutable field omitted", field)
		}
	}
	ordered := environmentOTARegistrationManifest{ServiceID: "ota", ManifestVersion: "fixture", ProtocolVersion: "1", EndpointRef: "ota-service", Options: []environmentOTARegistrationOption{{Code: "z", DisplayName: "Z", Requires: []string{"z", "a"}}, {Code: "a", DisplayName: "A"}}}
	reordered := ordered
	reordered.Options = []environmentOTARegistrationOption{{Code: "a", DisplayName: "A"}, {Code: "z", DisplayName: "Z", Requires: []string{"a", "z"}}}
	if ordered.digest() != reordered.digest() {
		t.Fatal("AM option/dependency order normalization differs")
	}
}

func TestOTARegistrationDescriptorRequiresVerifiedExactImageAndBindsPlanHash(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "old-artifact", "invalid-descriptor", "wrong-source", "wrong-digest", "wrong-endpoint", "wrong-registration-host", "wrong-registration-path", "unknown-descriptor-field"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				definitions, ota := otaStartupFixture(t, environment)
				sha, digest := strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)
				repo := "hkt999rtk/rtk_video_cloud"
				artifact := map[string]any{"schema": "rtk-service-image/v1", "repository": repo, "source_commit": sha, "run_id": "77", "run_attempt": "2", "image": "ghcr.io/" + repo + "/video-cloud-api", "digest": digest, "service_registration_manifests": map[string]any{"ota": otaRegistrationManifestFixture()}}
				switch mode {
				case "old-artifact":
					delete(artifact, "service_registration_manifests")
				case "invalid-descriptor":
					artifact["service_registration_manifests"] = map[string]any{"ota": map[string]any{"service_id": "ota", "manifest_version": "private-canary"}}
				case "wrong-source":
					artifact["source_commit"] = strings.Repeat("c", 40)
				case "wrong-digest":
					artifact["images"] = map[string]any{"api": map[string]any{"image": artifact["image"], "digest": "sha256:" + strings.Repeat("c", 64)}}
				case "unknown-descriptor-field":
					raw, _ := json.Marshal(otaRegistrationManifestFixture())
					var fields map[string]any
					_ = json.Unmarshal(raw, &fields)
					fields["private_canary"] = "private-canary"
					artifact["service_registration_manifests"] = map[string]any{"ota": fields}
				}
				raw, _ := json.Marshal(artifact)
				image, err := selectEnvironmentPublishedImage(raw, repo, 77, 2, sha, "ghcr.io/"+repo+"/video-cloud-api", false)
				if mode == "wrong-source" || mode == "wrong-digest" || mode == "invalid-descriptor" || mode == "unknown-descriptor-field" {
					if err == nil {
						t.Fatal("unverified/malformed descriptor accepted", mode)
					}
					if strings.Contains(err.Error(), "private-canary") {
						t.Fatal("private artifact leaked")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				otaStartupContainer(ota)["image"] = image.Reference
				if mode == "wrong-endpoint" {
					otaStartupContainer(ota)["env"] = mergeEnvironmentNamedFields(otaStartupContainer(ota)["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_OTA_SERVICE_ENDPOINT_REF", "value": "wrong"}})
				}
				if mode == "wrong-registration-host" || mode == "wrong-registration-path" {
					value := mqttFoundationEnv(otaStartupContainer(ota), "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL")
					if mode == "wrong-registration-host" {
						value = "https://foreign.example:8443"
					} else {
						value += "/foreign"
					}
					otaStartupContainer(ota)["env"] = mergeEnvironmentNamedFields(otaStartupContainer(ota)["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL", "value": value}})
				}
				release := environmentRelease{Environment: environment, Stack: "video-cloud-" + environment, Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}
				err = validateEnvironmentOTARegistrationRelease(definitions, release)
				if (err == nil) != (mode == "valid") {
					t.Fatal("enabled OTA descriptor decision wrong", mode, err)
				}
				if mode == "valid" {
					before := deploymentplan.Digest(release)
					image.OTARegistration.Options[0].Description += " changed"
					if before == deploymentplan.Digest(release) {
						t.Fatal("descriptor not bound into release hash")
					}
					p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{ReleaseSHA256: before}}
					old := p.SHA256()
					p.ReleaseSHA256 = deploymentplan.Digest(release)
					if old == p.SHA256() {
						t.Fatal("descriptor not bound into plan hash")
					}
				}
			})
		}
	}
	if err := validateEnvironmentOTARegistrationRelease(nil, environmentRelease{}); err != nil {
		t.Fatal("disabled OTA requires a new descriptor", err)
	}
}

func TestOTARegistrationPublicationRechecksFrozenDescriptor(t *testing.T) {
	for _, mode := range []string{"valid", "changed", "missing"} {
		t.Run(mode, func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			repo := "hkt999rtk/rtk_video_cloud"
			digest := "sha256:" + strings.Repeat("b", 64)
			image := environmentReleaseImage{Reference: "ghcr.io/" + repo + "/video-cloud-api@" + digest, SourceRevision: sha, OTARegistration: otaRegistrationManifestFixture()}
			fresh := otaRegistrationManifestFixture()
			if mode == "changed" {
				fresh.Options[0].Description = "different content"
			}
			artifact := map[string]any{"schema": "rtk-service-image/v1", "repository": repo, "source_commit": sha, "run_id": "77", "run_attempt": "2", "image": "ghcr.io/" + repo + "/video-cloud-api", "digest": digest, "service_registration_manifests": map[string]any{"ota": fresh}}
			if mode == "missing" {
				delete(artifact, "service_registration_manifests")
			}
			raw, _ := json.Marshal(artifact)
			archive := publicationZIP(t, "image-manifest.json", raw)
			run := func(_ context.Context, command string, args ...string) ([]byte, error) {
				if command != "gh" {
					t.Fatal("unexpected provenance command")
				}
				if strings.HasSuffix(args[1], "/runs/77") {
					return []byte(`{"id":77,"head_sha":"` + sha + `","head_branch":"main","event":"push","status":"completed","conclusion":"success","run_attempt":2}`), nil
				}
				if strings.HasSuffix(args[1], "/artifacts?per_page=100") {
					return []byte(`{"total_count":1,"artifacts":[{"id":99,"name":"image-manifest","size_in_bytes":500,"expired":false}]}`), nil
				}
				return archive, nil
			}
			_, err := verifyEnvironmentPublication(context.Background(), repo, 77, image, false, run)
			if (err == nil) != (mode == "valid") {
				t.Fatal("frozen descriptor verification wrong", mode, err)
			}
		})
	}
}

func otaRegistrationPrerequisiteFixture(t *testing.T, environment string) (*deploymentplan.Plan, environmentRelease, secretStore, map[string]deploymentplan.Object, map[string]any) {
	t.Helper()
	_, ota := otaStartupFixture(t, environment)
	stack := "video-cloud-" + environment
	_, _, release := completeEnvironmentRenderFixture(environment)
	otaStartupContainer(ota)["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	certs := newLKEPlatformCertificateFixture(t, map[string]string{"CLOUD_STACK_NAME": stack})
	identity := deploymentplan.Clone(certs.identities["service:ota"])
	leaf, _ := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(identity["data"])["client.crt"]))
	leaf = append(leaf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs.issuer.Raw})...)
	deploymentplan.Map(identity["data"])["client.crt"] = base64.StdEncoding.EncodeToString(leaf)
	identity["kind"] = "Secret"
	identity["metadata"] = deploymentplan.Object{"name": otaServiceIdentitySecretName, "namespace": stack + "-video-cloud", "uid": "original-identity"}
	am := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": stack + "-account-manager", "uid": "original-am"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "account-manager-runtime"}}}}}}}}}
	amApp := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(am)["containers"])[0])
	amApp["env"] = []any{deploymentplan.Object{"name": "ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_CERT", "value": "/etc/registration/tls.crt"}}
	amApp["volumeMounts"] = []any{deploymentplan.Object{"name": "account-manager-service-registration-tls", "mountPath": "/etc/registration", "readOnly": true}}
	environmentPKIPod(am)["volumes"] = []any{deploymentplan.Object{"name": "account-manager-service-registration-tls", "secret": deploymentplan.Object{"secretName": serviceRegistrationTLSSecretName}}}
	serverDNS := "account-manager." + stack + "-account-manager.svc"
	serverKey, serverCert := lkeTestLeaf(t, certs.issuer, certs.issuerKey, big.NewInt(88), pkix.Name{CommonName: "original-registration"}, []string{serverDNS, serverDNS + ".cluster.local"}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, certs.now)
	amTLS := deploymentplan.Clone(lkeTestSecret(map[string][]byte{"tls.crt": serverCert, "tls.key": serverKey}))
	amTLS["kind"] = "Secret"
	amTLS["metadata"] = deploymentplan.Object{"name": serviceRegistrationTLSSecretName, "namespace": stack + "-account-manager", "uid": "original-am-tls"}
	runtime := deploymentplan.Object{"kind": "Secret", "metadata": deploymentplan.Object{"name": "account-manager-runtime", "namespace": stack + "-account-manager", "uid": "original-runtime"}, "data": deploymentplan.Object{"DATABASE_URL": base64.StdEncoding.EncodeToString([]byte("postgres://postgres:private-canary@postgresql." + stack + "-platform.svc.cluster.local:5432/rtk_account_manager?sslmode=disable"))}}
	owner := deploymentplan.Object{"kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "postgresql", "namespace": stack + "-platform", "uid": "original-database"}}
	amService := deploymentplan.Object{"kind": "Service", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": stack + "-account-manager", "uid": "original-am-service"}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": "service-reg", "port": float64(8443), "targetPort": "service-reg"}}}}
	objects := map[string]deploymentplan.Object{}
	p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ReleaseSHA256: deploymentplan.Digest(release)}}
	for _, o := range []deploymentplan.Object{ota, identity, am, amService, amTLS, runtime, owner} {
		id := deploymentplan.ID(o)
		objects[id] = o
		uid := deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
		if id == deploymentplan.ID(ota) {
			uid = "original-ota"
		}
		p.Resources = append(p.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: uid, Desired: deploymentplan.Clone(o), Baseline: deploymentplan.Clone(o)})
	}
	objects["Pod/"+stack+"-platform/postgresql-0"] = deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": "postgresql-0", "namespace": stack + "-platform", "uid": "original-db-pod", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "uid": "original-database", "controller": true}}}, "status": deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Ready", "status": "False"}}}}
	m := release.Images["LKE_VIDEO_CLOUD_IMAGE"].OTARegistration
	state := map[string]any{"schema": "public", "candidate": otaRegistrationStoredFixture(m), "publication": map[string]any{"published_version": m.ManifestVersion, "status": "suspended", "manifest": otaRegistrationStoredFixture(m)}, "workload": map[string]any{"service_id": "ota", "instance_id": otaServiceInstanceID, "allowed_option_codes": []string{"ota"}, "revoked": false}, "mqtt_defined": true, "ota_option_conflict": false}
	store, err := newSecretStore(t.TempDir(), environment)
	if err != nil {
		t.Fatal(err)
	}
	return p, release, store, objects, state
}

func TestOTARegistrationChecksImmutableDefinitionAndOriginalApprovalWithoutFutureReady(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid-suspended", "valid-active", "valid-hostname-alias", "unapproved-hostname-alias", "new-version", "immutable-conflict", "same-version-content-change", "content-digest-corrupt", "published-corrupt", "denied", "revoked", "wrong-instance", "wrong-service", "wrong-options", "wrong-owner", "identity-drift", "wrong-db-owner", "wrong-release", "wrong-db", "wrong-schema-option", "wrong-schema-query", "malformed-query", "db-fragment", "candidate-db-change", "changed-pod", "private-sql-error", "malformed", "missing-dependency", "option-conflict", "cancelled"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				p, release, store, objects, state := otaRegistrationPrerequisiteFixture(t, environment)
				stack := p.Stack
				manifest := release.Images["LKE_VIDEO_CLOUD_IMAGE"].OTARegistration
				switch mode {
				case "valid-hostname-alias", "unapproved-hostname-alias":
					for _, r := range p.Resources {
						if c := otaStartupContainer(r.Desired); c != nil {
							value := strings.ReplaceAll(mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL"), ".svc.cluster.local", ".svc")
							c["env"] = mergeEnvironmentNamedFields(c["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL", "value": value}})
						}
					}
					if mode == "unapproved-hostname-alias" {
						certs := newLKEPlatformCertificateFixture(t, map[string]string{"CLOUD_STACK_NAME": stack})
						deploymentplan.Map(objects["Secret/"+stack+"-account-manager/"+serviceRegistrationTLSSecretName]["data"])["tls.crt"] = deploymentplan.Map(certs.listener["data"])["tls.crt"]
					}
				case "valid-active":
					state["publication"].(map[string]any)["status"] = "active"
				case "immutable-conflict":
					state["candidate"].(map[string]any)["digest_sha256"] = strings.Repeat("a", 64)
				case "same-version-content-change":
					changed := otaRegistrationManifestFixture()
					changed.Options[0].Description = "Prior immutable content for this same version"
					state["candidate"] = otaRegistrationStoredFixture(changed)
				case "content-digest-corrupt":
					state["candidate"].(map[string]any)["endpoint_ref"] = "changed"
				case "published-corrupt":
					state["publication"].(map[string]any)["manifest"] = nil
				case "denied":
					state["workload"] = nil
				case "revoked":
					state["workload"].(map[string]any)["revoked"] = true
				case "wrong-instance":
					state["workload"].(map[string]any)["instance_id"] = "foreign"
				case "wrong-service":
					state["workload"].(map[string]any)["service_id"] = "foreign"
				case "wrong-options":
					state["workload"].(map[string]any)["allowed_option_codes"] = []string{"foreign"}
				case "wrong-owner":
					deploymentplan.Map(objects["Secret/"+stack+"-video-cloud/"+otaServiceIdentitySecretName]["metadata"])["uid"] = "replacement"
				case "identity-drift":
					deploymentplan.Map(objects["Secret/"+stack+"-video-cloud/"+otaServiceIdentitySecretName]["data"])["client.crt"] = "private-canary"
				case "wrong-db-owner":
					deploymentplan.Map(objects["StatefulSet/"+stack+"-platform/postgresql"]["metadata"])["uid"] = "replacement"
				case "wrong-db":
					deploymentplan.Map(objects["Secret/"+stack+"-account-manager/account-manager-runtime"]["data"])["DATABASE_URL"] = base64.StdEncoding.EncodeToString([]byte("postgres://postgres:private-canary@foreign.example:5432/rtk_account_manager"))
				case "wrong-schema-option", "wrong-schema-query", "malformed-query", "db-fragment":
					data := deploymentplan.Map(objects["Secret/"+stack+"-account-manager/account-manager-runtime"]["data"])
					dsn, _ := base64.StdEncoding.DecodeString(deploymentplan.String(data["DATABASE_URL"]))
					appendix := map[string]string{"wrong-schema-option": "&options=-csearch_path%3Dother", "wrong-schema-query": "&search_path=other", "malformed-query": "&options=%zz", "db-fragment": "#other-schema"}[mode]
					data["DATABASE_URL"] = base64.StdEncoding.EncodeToString(append(dsn, []byte(appendix)...))
				case "candidate-db-change":
					for _, r := range p.Resources {
						if r.ID == "Deployment/"+stack+"-account-manager/account-manager" {
							deploymentplan.Map(certIssuerObjectList(environmentPKIPod(r.Desired)["containers"])[0])["env"] = []any{deploymentplan.Object{"name": "DATABASE_URL", "value": "private-canary"}}
						}
					}
				case "new-version", "missing-dependency", "option-conflict":
					manifest.ManifestVersion = "3"
					state["candidate"] = nil
					if mode == "missing-dependency" {
						state["mqtt_defined"] = false
					}
					if mode == "option-conflict" {
						state["ota_option_conflict"] = true
					}
				}
				p.ReleaseSHA256 = deploymentplan.Digest(release)
				if mode == "wrong-release" {
					p.ReleaseSHA256 = "unreviewed-release"
				}
				before := deploymentplan.Digest(state)
				reads, queries := 0, 0
				read := func(_ context.Context, id string) (deploymentplan.Object, error) {
					reads++
					if strings.Contains(id, "otaservice") || strings.Contains(id, "lease") {
						t.Fatal("qualification required future OTA runtime", id)
					}
					o := deploymentplan.Clone(objects[id])
					if mode == "changed-pod" && strings.HasPrefix(id, "Pod/") && queries > 0 {
						deploymentplan.Map(o["metadata"])["uid"] = "replacement"
					}
					return o, nil
				}
				command := func(_ context.Context, args ...string) ([]byte, error) {
					queries++
					query := args[len(args)-1]
					if !strings.HasPrefix(query, "BEGIN TRANSACTION READ ONLY;") || !strings.Contains(query, "statement_timeout='10s'") || !strings.Contains(query, "COMMIT;") || strings.Contains(strings.Join(args, " "), "private-canary") {
						t.Fatal("query writes, leaks credentials, or lacks a deadline")
					}
					for _, verb := range []string{"INSERT ", "UPDATE ", "DELETE ", "CREATE ", "lease_expires_at", "ready"} {
						if strings.Contains(query, verb) {
							t.Fatal("query depends on runtime/changes registry", verb)
						}
					}
					if mode == "private-sql-error" {
						return nil, errors.New("private-canary")
					}
					if mode == "malformed" {
						return []byte("private-canary"), nil
					}
					raw, _ := json.Marshal(state)
					return raw, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				err := checkEnvironmentOTARegistration(ctx, p, release, store, read, command)
				pass := mode == "valid-suspended" || mode == "valid-active" || mode == "valid-hostname-alias" || mode == "new-version"
				if (err == nil) != pass {
					t.Fatal("registration candidate decision wrong", mode, err)
				}
				if err != nil {
					var b *deploymentplan.Blocker
					if !errors.As(err, &b) || b.ID != "deployment.compatibility.ota-registration" || strings.Contains(err.Error(), "private-canary") {
						t.Fatal("unstable/private diagnostic", err)
					}
				}
				if pass && (queries != 1 || before != deploymentplan.Digest(state)) {
					t.Fatal("qualification mutated state or repeated SQL")
				}
				if mode == "cancelled" && reads != 0 {
					t.Fatal("cancelled qualification still read prerequisites")
				}
			})
		}
	}
}

func TestOTARegistrationUsesFrozenKubeconfigAndHonorsDeadline(t *testing.T) {
	for _, mode := range []string{"valid", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			p, release, _, objects, state := otaRegistrationPrerequisiteFixture(t, "dev")
			config, _ := yaml.Marshal(frozenKubernetesFixture(t))
			backend, err := newEnvironmentKubernetesBackend(string(config), "dev")
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.Store.write("kube/kubeconfig.yaml", []byte("must-not-be-consumed"), true); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(state)
			body := "selected=\nwhile test $# -gt 0; do if test \"$1\" = --kubeconfig; then shift; selected=\"$1\"; break; fi; shift; done\ncase \"$(cat \"$selected\")\" in *private-frozen-token*) :;; *) exit 99;; esac\nprintf '%s' '" + string(raw) + "'\n"
			if mode == "deadline" {
				body = "sleep 3\n"
			}
			calls := deploymentRuntimeTestKubectl(t, body)
			bin := t.TempDir()
			if err := os.Symlink(os.Getenv("RTK_CLOUD_KUBECTL"), filepath.Join(bin, "kubectl")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			read := func(_ context.Context, id string) (deploymentplan.Object, error) {
				return deploymentplan.Clone(objects[id]), nil
			}
			ctx := context.Background()
			cancel := func() {}
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			}
			defer cancel()
			start := time.Now()
			err = checkEnvironmentOTARegistration(ctx, p, release, backend.Store, read, func(ctx context.Context, args ...string) ([]byte, error) { return backend.command(ctx, nil, args...) })
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
			if mode == "deadline" && time.Since(start) > 2*time.Second {
				t.Fatal("parent deadline ignored")
			}
			if mode == "valid" && deploymentRuntimeCallCount(t, calls) != 1 {
				t.Fatal("SQL was retried")
			}
			assertPlanAccessPrivateFilesRemoved(t, backend)
		})
	}
}

func TestOTARegistrationQueryQuotesCandidateVersionAndUsesOriginalIssuer(t *testing.T) {
	h := sha256.Sum256([]byte("synthetic-public-issuer"))
	query := otaRegistrationQuery("dev", "version'quote", "service:ota", hex.EncodeToString(h[:]))
	if !strings.Contains(query, "manifest_version='version''quote'") || !strings.Contains(query, "issuer_fingerprint_sha256='"+hex.EncodeToString(h[:])+"'") {
		t.Fatal("version quoting or original issuer missing")
	}
}
