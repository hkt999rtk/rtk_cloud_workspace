package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedApplicationIncomingManifestRepairPreservesOriginalPublicSource(t *testing.T) {
	cfg, p, release, bootstrap, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, bootstrap, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"device", "app"} {
		name := "video-cloud-api-pki"
		if domain == "app" {
			name = "video-cloud-api-app-pki"
		}
		t.Run(domain, func(t *testing.T) {
			wanted := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+name)
			role := domain + "-trust-manifest"
			for _, retainedAlias := range []bool{false, true} {
				original := deploymentplan.Clone(wanted)
				container := managedNamedField(environmentPKIPod(original)["containers"], "app")
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST": "/run/pki-" + domain + "/bundles.json"}))
				if !retainedAlias {
					container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], role)
					environmentPKIPod(original)["volumes"] = environmentCreateServiceWithoutNamed(environmentPKIPod(original)["volumes"], role)
				}
				before := deploymentplan.Digest(original)
				repaired, err := managedApplicationIncomingManifestRepair(original)
				if err != nil || deploymentplan.Digest(original) != before || deploymentplan.Digest(repaired) != deploymentplan.Digest(wanted) {
					t.Fatal("projection changed more than its exact original public file alias", err)
				}
				again, err := managedApplicationIncomingManifestRepair(repaired)
				if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(repaired) {
					t.Fatal("projection is not repeat-safe", err)
				}
				if domain == "app" {
					// The independent one-flag repair accepts both historical and
					// projected public files, and preserves their exact shape.
					for _, source := range []deploymentplan.Object{original, repaired} {
						before := deploymentplan.Digest(source)
						checked, err := managedApplicationAppCRLRoleRepair(source)
						if err != nil || deploymentplan.Digest(checked) != before {
							t.Fatal("App role guard changed the selected original manifest projection", err)
						}
					}
				}
			}
			for _, problem := range []string{"wrong-owner", "wrong-role", "no-mtls", "foreign-whole-source", "writable-whole-source", "foreign-field", "field-ref", "missing-alias", "half-alias", "foreign-alias-source", "foreign-alias-key", "foreign-alias-path", "foreign-alias-subpath", "writable-alias"} {
				t.Run(problem, func(t *testing.T) {
					object := deploymentplan.Clone(wanted)
					container := managedNamedField(environmentPKIPod(object)["containers"], "app")
					fields, _ := environmentPKINamedFields(container["env"])
					volumes, _ := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
					mounts, _ := environmentPKINamedFields(container["volumeMounts"])
					switch problem {
					case "wrong-owner":
						deploymentplan.Map(object["metadata"])["name"] = "video-cloud-api"
					case "wrong-role":
						fields["VIDEO_CLOUD_AUTH_APP_PKI_ENABLED"]["value"] = "unknown"
					case "no-mtls":
						fields["VIDEO_CLOUD_AUTH_MTLS_REQUIRED"]["value"] = "false"
					case "foreign-whole-source":
						deploymentplan.Map(volumes["pki-"+domain]["configMap"])["name"] = "foreign"
					case "writable-whole-source":
						mounts["pki-"+domain]["readOnly"] = false
					case "foreign-field":
						fields["VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST"]["value"] = "/foreign/bundles.json"
					case "field-ref":
						fields["VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST"]["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "foreign", "key": "path"}}
					case "missing-alias":
						container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], role)
						environmentPKIPod(object)["volumes"] = environmentCreateServiceWithoutNamed(environmentPKIPod(object)["volumes"], role)
					case "half-alias":
						container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], role)
					case "foreign-alias-source":
						deploymentplan.Map(volumes[role]["configMap"])["name"] = "foreign"
					case "foreign-alias-key":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(volumes[role]["configMap"])["items"])[0])["key"] = "foreign.json"
					case "foreign-alias-path":
						mounts[role]["mountPath"] = "/foreign/bundles.json"
					case "foreign-alias-subpath":
						mounts[role]["subPath"] = "foreign.json"
					case "writable-alias":
						mounts[role]["readOnly"] = false
					}
					before := deploymentplan.Digest(object)
					if result, err := managedApplicationIncomingManifestRepair(object); err == nil || result != nil || deploymentplan.Digest(object) != before {
						t.Fatal("foreign public binding escaped the exact repair")
					}
				})
			}
			container := managedNamedField(environmentPKIPod(wanted)["containers"], "app")
			file, err := coreMQTTFile(wanted, container, role, "bundles.json")
			if err != nil || file.kind != "ConfigMap" || file.reference != "pki-create-"+domain+"-trust" || file.file != "/run/pki-"+domain+"-manifest/bundles.json" {
				t.Fatal("final role lost the original public manifest", err)
			}
			mounts := certIssuerObjectList(container["volumeMounts"])
			for i, raw := range mounts {
				first := deploymentplan.String(deploymentplan.Map(raw)["mountPath"])
				for j, other := range mounts {
					second := deploymentplan.String(deploymentplan.Map(other)["mountPath"])
					if i != j && (first == second || strings.HasPrefix(second, strings.TrimSuffix(first, "/")+"/")) {
						t.Fatal("incoming projection overlaps an existing directory mount", first, second)
					}
				}
			}
		})
	}
}

func TestCreateFinalIncomingManifestSatisfiesActualNativeReader(t *testing.T) {
	cfg, p, release, bootstrap, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, bootstrap, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for domain, name := range map[string]string{"device": "video-cloud-api-pki", "app": "video-cloud-api-app-pki"} {
		object := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+name)
		paths[domain] = mqttFoundationEnv(managedNamedField(environmentPKIPod(object)["containers"], "app"), "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "public-paths.json")
	raw, _ := json.Marshal(paths)
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repos/rtk_video_cloud")
	virtual := filepath.Join(repo, "internal/apiapp/zz_deployment_incoming_manifest_overlay_test.go")
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("isolated native fixture would shadow a product source")
	}
	physical := filepath.Join(dir, "native_incoming_test.go")
	if err := os.WriteFile(physical, []byte(createApplicationNativeIncomingManifestTest), 0600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	raw, _ = json.Marshal(map[string]any{"Replace": map[string]string{virtual: physical}})
	if err := os.WriteFile(overlay, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin/go"), "test", "-overlay", overlay, "-vet=off", "./internal/apiapp", "-run", "^(TestDeploymentRenderedIncomingManifest|TestIncomingRootBootstrapInstallsAuthenticatedPolicyBeforeDynamicTLS)$", "-count=1")
	command.Dir = repo
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "VIDEO_CLOUD_TEST_DSN=", "RTK_DEPLOYMENT_INCOMING_MANIFEST_FIXTURE="+fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual native incoming reader rejected the original public projection: %v\n%s", err, output)
	}
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("native fixture modified the original product checkout")
	}
}

const createApplicationNativeIncomingManifestTest = `package apiapp
import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "encoding/pem"
 "os"
 "path/filepath"
 "testing"
 "video_cloud/internal/pki"
 "video_cloud/internal/pkitrust"
)
func TestDeploymentRenderedIncomingManifest(t *testing.T) {
 raw, err := os.ReadFile(os.Getenv("RTK_DEPLOYMENT_INCOMING_MANIFEST_FIXTURE"))
 var paths map[string]string
 if err != nil || json.Unmarshal(raw, &paths) != nil || len(paths) != 2 { t.Fatal("isolated public renderer fixture unavailable") }
 for domain, rendered := range paths {
  t.Run(domain, func(t *testing.T) {
   if rendered != "/run/pki-"+domain+"-manifest/bundles.json" { t.Fatal("finalizer lost its native regular file alias") }
   cfg := bootstrapAPITrustFixture(t)
   if domain == "app" { cfg.Auth.ProductPKIEnabled, cfg.Auth.AppPKIEnabled = false, true }
   root, _ := generateAPITestCA(t, "original-incoming-root")
   roots := string(pem.EncodeToMemory(&pem.Block{Type:"CERTIFICATE", Bytes:root.Raw}))
   sum := sha256.Sum256([]byte(roots))
   issuer := pki.Issuer{ID:"00000000-0000-4000-8000-000000000001", Scope:pki.Scope{Environment:"dev", Domain:domain}, Kind:"root", Status:"active", Certificate:roots, Chain:roots, Fingerprint:pki.Fingerprint(root), BundleVersion:hex.EncodeToString(sum[:])}
   manifest, _ := json.Marshal([]pkitrust.BundleReference{{IssuerID:issuer.ID, Version:issuer.BundleVersion}})
   dir := t.TempDir()
   target := filepath.Join(dir, "atomic-data-bundles.json")
   if err := os.WriteFile(target, manifest, 0644); err != nil { t.Fatal(err) }
   old := filepath.Join(dir, "projected-bundles.json")
   if err := os.Symlink(target, old); err != nil { t.Fatal(err) }
   cfg.Auth.IssuerTrustManifest = old
   lookup := func(context.Context, string) (pki.Issuer,error) { return issuer,nil }
   if _, err := incomingReviewedRoot(context.Background(), cfg, lookup, roots, true); err == nil || err.Error() != "invalid original incoming trust material" { t.Fatal("historical AtomicWriter control did not reproduce exact native failure", err) }
   alias := filepath.Join(dir, filepath.FromSlash(rendered[1:]))
   if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil { t.Fatal(err) }
   // A hard link models subPath's regular-file observation without changing
   // any bytes or relaxing the product's Lstat/public authority checks.
   if err := os.Link(target, alias); err != nil { t.Fatal(err) }
   cfg.Auth.IssuerTrustManifest = alias
   accepted, err := incomingReviewedRoot(context.Background(), cfg, lookup, roots, true)
   if err != nil || accepted.ID != issuer.ID { t.Fatal("native signature/registry/bundle verification rejected identical regular public bytes", err) }
   original := incomingRootBootstrap{Environment:"dev", Domain:domain, RootID:issuer.ID, BundleVersion:issuer.BundleVersion, RootsPEM:roots}
   admission := filepath.Join(dir, "incoming-root-bootstrap.json")
   if err := publishIncomingBootstrap(admission, original, true); err != nil { t.Fatal(err) }
   var read incomingRootBootstrap
   if err := readIncomingBootstrapJSON(admission, &read, true); err != nil || read.RootID != issuer.ID { t.Fatal("native original private admission changed", err) }
   privateLink := filepath.Join(dir, "private-link.json")
   if err := os.Symlink(admission, privateLink); err != nil { t.Fatal(err) }
   if err := readIncomingBootstrapJSON(privateLink, &read, true); err == nil { t.Fatal("private admission symlink was accepted") }
   if err := os.Chmod(admission, 0644); err != nil { t.Fatal(err) }
   if err := readIncomingBootstrapJSON(admission, &read, true); err == nil { t.Fatal("private admission mode guard was weakened") }
  })
 }
}
`
