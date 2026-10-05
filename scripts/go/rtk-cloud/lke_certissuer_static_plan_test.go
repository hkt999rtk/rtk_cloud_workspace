package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

type certIssuerStaticPlanFixture struct {
	env              map[string]string
	paths            provisionPaths
	material         lkeCertIssuerMaterial
	service          certIssuerIngressObject
	deployment       certIssuerIngressObject
	ingresses        *certIssuerMigrationFixture
	runtimeSecretUID string
	deploymentReads  int
	beforeRead       func(string)
}

func newCertIssuerStaticPlanFixture(t *testing.T) *certIssuerStaticPlanFixture {
	t.Helper()
	f := &certIssuerStaticPlanFixture{
		env:   map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-staging.realtekconnect.com", "CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519", "LKE_RUNTIME_SECRET_SEED": "fixture-seed"},
		paths: certIssuerDesiredMaterialTestPaths(t),
	}
	// Use the real renderer's public Service/Deployment fixture, including API
	// UID/resourceVersion and the same mounted certificate paths as deployment.
	f.material = fakeKubectlCertIssuerServing(t)
	if err := json.Unmarshal([]byte(os.Getenv("FAKE_CERTISSUER_SERVICE_JSON")), &f.service); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(os.Getenv("FAKE_CERTISSUER_DEPLOYMENT_JSON")), &f.deployment); err != nil {
		t.Fatal(err)
	}
	if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(f.paths, "certissuer"), f.material); err != nil {
		t.Fatal(err)
	}
	legacy := certIssuerCanonicalIngress(f.env, lkeResolveCertIssuerTLSConfig(f.env))
	metadata := certIssuerObjectMap(legacy["metadata"])
	metadata["name"], metadata["namespace"], metadata["uid"], metadata["resourceVersion"] = "video-cloud-staging-certissuer", lkeIngressNamespace(f.env), "legacy-uid", "1"
	metadata["annotations"] = map[string]any{"nginx.ingress.kubernetes.io/backend-protocol": "HTTPS"}
	rule := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(legacy["spec"])["rules"])[0])
	path := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(rule["http"])["paths"])[0])
	certIssuerObjectMap(certIssuerObjectMap(path["backend"])["service"])["name"] = "public-certissuer-video-cloud"
	f.ingresses = newCertIssuerIngressFixture(legacy)
	return f
}

func (f *certIssuerStaticPlanFixture) command(t *testing.T) certIssuerIngressCommand {
	t.Helper()
	return func(input io.Reader, args ...string) ([]byte, error) {
		if input != nil {
			t.Fatal("static preflight attempted a mutation")
		}
		call := strings.Join(args, " ")
		if f.beforeRead != nil {
			f.beforeRead(call)
		}
		switch {
		case strings.Contains(call, "get deployment certissuer --ignore-not-found=true -o json"):
			f.deploymentReads++
			if f.deployment == nil {
				return nil, nil
			}
			return json.Marshal(f.deployment)
		case strings.Contains(call, "get service certissuer --ignore-not-found=true -o json"):
			if f.service == nil {
				return nil, nil
			}
			return json.Marshal(f.service)
		case strings.Contains(call, "get secret certissuer-runtime -o jsonpath={.data.tls\\.crt}"):
			return []byte(base64.StdEncoding.EncodeToString([]byte(f.material.ServerCert))), nil
		case strings.Contains(call, "get secret certissuer-runtime -o jsonpath={.data.client-ca\\.crt}"):
			return []byte(base64.StdEncoding.EncodeToString([]byte(f.material.ServiceCA))), nil
		case strings.Contains(call, "get secret certissuer-runtime --ignore-not-found=true -o jsonpath={.metadata.uid}"):
			return []byte(f.runtimeSecretUID), nil
		default:
			return nil, fmt.Errorf("unexpected static preflight public read: %s", call)
		}
	}
}

func (f *certIssuerStaticPlanFixture) access(t *testing.T) certIssuerIngressIO {
	t.Helper()
	access := f.ingresses.access()
	command := f.command(t)
	access.serving = func(config lkeCertIssuerTLSConfig, allowAbsent, requireAvailable bool) error {
		return certIssuerValidateInstalledServingPolicy(command, config, allowAbsent, requireAvailable)
	}
	return access
}

func (f *certIssuerStaticPlanFixture) plan(t *testing.T) (*lkeCertIssuerIngressMigration, error) {
	t.Helper()
	return lkePlanCertIssuerIngressMigrationForStaticRenderWithIO(context.Background(), f.paths, f.env, f.command(t), f.access(t))
}

func certIssuerStaticPlanContainer(deployment certIssuerIngressObject) map[string]any {
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	return certIssuerObjectMap(certIssuerObjectList(pod["containers"])[0])
}

func TestCertIssuerStaticRenderPlanRepairsMutableTopology(t *testing.T) {
	for _, scenario := range []string{"missing-service", "wrong-target-port", "wrong-listener"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCertIssuerStaticPlanFixture(t)
			service := f.service
			port := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(service["spec"])["ports"])[0])
			container := certIssuerStaticPlanContainer(f.deployment)
			var listener map[string]any
			for _, item := range certIssuerObjectList(container["env"]) {
				entry := certIssuerObjectMap(item)
				if entry["name"] == "CERT_ISSUER_LISTEN_ADDR" {
					listener = entry
				}
			}
			switch scenario {
			case "missing-service":
				f.service = nil
			case "wrong-target-port":
				port["targetPort"] = 4040
			case "wrong-listener":
				listener["value"] = ":4040"
			}
			before := certIssuerDesiredMaterialSnapshot(t, f.paths)
			plan, err := f.plan(t)
			if err != nil {
				t.Fatal("qualified full-static preflight blocked repairable topology:", err)
			}
			if _, err := lkePlanCertIssuerIngressMigrationWithIO(f.paths, f.env, f.access(t)); err == nil {
				t.Fatal("route-only/targeted planning accepted incorrect installed topology")
			}
			if err := plan.Apply(); err == nil || len(f.ingresses.ops) != 0 {
				t.Fatalf("migration ran before the workload repaired serving policy: err=%v ops=%v", err, f.ingresses.ops)
			}
			f.service = service
			port["targetPort"] = lkeResolveCertIssuerTLSConfig(f.env).HTTPSPort
			listener["value"] = ":9443"
			if err := plan.Apply(); err != nil {
				t.Fatal("post-render migration did not accept corrected installed topology:", err)
			}
			if err := plan.Verify(); err != nil {
				t.Fatal(err)
			}
			if after := certIssuerDesiredMaterialSnapshot(t, f.paths); !reflect.DeepEqual(before, after) {
				t.Fatal("planning or ingress migration regenerated identity material")
			}
			ops := len(f.ingresses.ops)
			repeat, err := f.plan(t)
			if err != nil {
				t.Fatal(err)
			}
			if err := repeat.Apply(); err != nil || len(f.ingresses.ops) != ops {
				t.Fatalf("repeat deployment changed converged ingress: %v", err)
			}
		})
	}
}

func TestCertIssuerStaticRenderPlanRetainsIdentityAndOwnershipFences(t *testing.T) {
	for _, scenario := range []string{"foreign-service", "foreign-deployment", "foreign-ingress", "managed-deployment", "identity-mismatch", "missing-identity-key", "headless-service", "deployment-disappears"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCertIssuerStaticPlanFixture(t)
			switch scenario {
			case "foreign-service":
				certIssuerObjectMap(certIssuerObjectMap(f.service["metadata"])["labels"])["rtk.realtek.com/stack"] = "other-stack"
			case "foreign-deployment":
				certIssuerObjectMap(certIssuerObjectMap(f.deployment["metadata"])["labels"])["rtk.realtek.com/provider"] = "other-provider"
			case "foreign-ingress":
				for _, obj := range f.ingresses.objects {
					certIssuerObjectMap(certIssuerObjectMap(obj["metadata"])["labels"])["rtk.realtek.com/stack"] = "other-stack"
				}
			case "managed-deployment":
				container := certIssuerStaticPlanContainer(f.deployment)
				container["env"] = append(certIssuerObjectList(container["env"]), map[string]any{"name": "CERT_ISSUER_HOST_IDENTITY_STATE", "value": "/var/lib/pki/server.json"})
			case "identity-mismatch":
				other, err := newLKECertIssuerMaterial(f.env)
				if err != nil {
					t.Fatal(err)
				}
				if err := replaceLKECertIssuerMaterial(sensitiveEnvironmentPath(f.paths, "certissuer"), other); err != nil {
					t.Fatal(err)
				}
			case "missing-identity-key":
				if err := os.Remove(sensitiveEnvironmentPath(f.paths, "certissuer", "server.key")); err != nil {
					t.Fatal(err)
				}
			case "headless-service":
				certIssuerObjectMap(f.service["spec"])["clusterIP"] = "None"
			case "deployment-disappears":
				f.beforeRead = func(call string) {
					if strings.Contains(call, "get deployment certissuer ") && f.deploymentReads != 0 {
						f.deployment = nil
					}
				}
			}
			before := certIssuerDesiredMaterialSnapshot(t, f.paths)
			if _, err := f.plan(t); err == nil {
				t.Fatal("full-static planning bypassed identity or ownership qualification")
			}
			if len(f.ingresses.ops) != 0 || !reflect.DeepEqual(before, certIssuerDesiredMaterialSnapshot(t, f.paths)) {
				t.Fatal("failed qualification changed resources or identity material")
			}
			if _, err := os.Stat(certIssuerIngressJournalDir(f.paths)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("read-only failed qualification wrote a migration journal")
			}
		})
	}
}

func TestCertIssuerStaticRenderPlanBootstrapRequiresNoExistingOwner(t *testing.T) {
	for _, scenario := range []string{"empty-environment", "legacy-ingress", "canonical-ingress", "service-only", "surviving-secret"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCertIssuerStaticPlanFixture(t)
			f.deployment = nil
			if scenario != "service-only" {
				f.service = nil
			}
			if err := os.RemoveAll(sensitiveEnvironmentPath(f.paths, "certissuer")); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "empty-environment", "service-only", "surviving-secret":
				f.ingresses = newCertIssuerIngressFixture()
			case "canonical-ingress":
				canonical := certIssuerCanonicalIngress(f.env, lkeResolveCertIssuerTLSConfig(f.env))
				metadata := certIssuerObjectMap(canonical["metadata"])
				metadata["uid"], metadata["resourceVersion"] = "canonical-uid", "1"
				f.ingresses = newCertIssuerIngressFixture(canonical)
			}
			if scenario == "surviving-secret" {
				f.runtimeSecretUID = "existing-runtime-secret"
			}
			_, err := f.plan(t)
			if scenario == "empty-environment" && err != nil {
				t.Fatal("truly empty environment was not deployable:", err)
			}
			if scenario != "empty-environment" && (err == nil || !strings.Contains(err.Error(), "identity source")) {
				t.Fatalf("existing owner without identity source allowed CA bootstrap: %v", err)
			}
			if _, err := os.Stat(sensitiveEnvironmentPath(f.paths, "certissuer")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("read-only qualification generated or restored a CA")
			}
			if len(f.ingresses.ops) != 0 {
				t.Fatal("bootstrap qualification changed ingress resources")
			}
		})
	}
}

func TestCertIssuerStaticRenderPlanCancellationStartsNoRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lkePlanCertIssuerIngressMigrationForStaticRenderWithIO(ctx, provisionPaths{}, certIssuerIngressTestEnv(), func(io.Reader, ...string) ([]byte, error) {
		t.Fatal("cancelled planning started a Kubernetes read")
		return nil, nil
	}, certIssuerIngressIO{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v; wanted cancellation", err)
	}
}
