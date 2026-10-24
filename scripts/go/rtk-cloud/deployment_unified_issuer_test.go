package main

import (
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func attachEnvironmentIssuerFixture(bindings *environmentIdentityBindings) {
	for i := range bindings.Pods {
		b := &bindings.Pods[i]
		if b.Name != "certissuer" && b.Name != "factoryenroll" {
			continue
		}
		b.IssuerRuntimeSecret = b.Name + "-runtime"
		if b.PodFields == nil {
			b.PodFields = deploymentplan.Object{}
		}
		if b.Name == "certissuer" {
			b.PodFields["volumes"] = []any{deploymentplan.Object{"name": "issuer-material", "secret": deploymentplan.Object{"secretName": b.IssuerRuntimeSecret}}}
			b.Containers[0].Fields["volumeMounts"] = []any{deploymentplan.Object{"name": "issuer-material", "mountPath": "/private/original-issuer", "readOnly": true}}
		} else {
			b.Containers[0].Fields["env"] = mergeEnvironmentNamedFields(b.Containers[0].Fields["env"], []any{deploymentplan.Object{"name": "FACTORY_ENROLL_AUTH_KEY", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": b.IssuerRuntimeSecret, "key": "FACTORY_ENROLL_AUTH_KEY"}}}})
		}
	}
}

func issuerRuntimeFixture(t *testing.T, environment string) (map[string]string, environmentPodBindings, deploymentplan.Object) {
	t.Helper()
	env, bindings, release := completeEnvironmentRenderFixture(environment)
	for _, b := range bindings.Pods {
		if b.Name == "certissuer" {
			o, err := renderManagedEnvironmentComponent(env, b)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(o, bindings, release); err != nil {
				t.Fatal(err)
			}
			return env, b, o
		}
	}
	t.Fatal("issuer fixture unavailable")
	return nil, environmentPodBindings{}, nil
}

func TestIssuerRuntimeDerivesMountedPathsAndConsistentPortForAllEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"directory", "renamed-file", "file-subpath", "missing-key", "duplicate-mount", "writable", "traversal", "manual-path", "manual-port"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				env, b, object := issuerRuntimeFixture(t, environment)
				pod := environmentPKIPod(object)
				c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				v := deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])
				m := deploymentplan.Map(certIssuerObjectList(c["volumeMounts"])[0])
				want := "/private/original-issuer/device-ca.crt"
				switch mode {
				case "renamed-file":
					deploymentplan.Map(v["secret"])["items"] = []any{deploymentplan.Object{"key": "device-ca.crt", "path": "issuer/ca.pem"}}
					want = "/private/original-issuer/issuer/ca.pem"
				case "file-subpath":
					m["mountPath"], m["subPath"] = "/private/issuer.pem", "device-ca.crt"
					want = "/private/issuer.pem"
				case "missing-key":
					deploymentplan.Map(v["secret"])["items"] = []any{deploymentplan.Object{"key": "wrong-ca", "path": "device-ca.crt"}}
				case "duplicate-mount":
					c["volumeMounts"] = append(certIssuerObjectList(c["volumeMounts"]), deploymentplan.Object{"name": m["name"], "mountPath": "/other", "readOnly": true})
				case "writable":
					m["readOnly"] = false
				case "traversal":
					deploymentplan.Map(v["secret"])["items"] = []any{deploymentplan.Object{"key": "device-ca.crt", "path": "../private-marker"}}
				case "manual-path":
					env["CERT_ISSUER_CA_CERT_PATH"] = "private-marker"
				case "manual-port":
					env["LKE_CERT_ISSUER_PORT"] = "1234"
				}
				err := applyEnvironmentIssuerRuntime(env, object, b)
				allowed := mode == "directory" || mode == "renamed-file" || mode == "file-subpath"
				if (err == nil) != allowed {
					t.Fatal("issuer path qualification decision changed", err)
				}
				if err != nil {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.issuer-runtime" || strings.Contains(err.Error(), "private-marker") {
						t.Fatal("invalid issuer reference lost its safe diagnostic", err)
					}
					return
				}
				if managedNamedField(c["env"], "CERT_ISSUER_CA_CERT_PATH")["value"] != want || managedNamedField(c["env"], "CERT_ISSUER_LISTEN_ADDR")["value"] != ":9443" || deploymentplan.Map(certIssuerObjectList(c["ports"])[0])["containerPort"] != float64(lkeResolveCertIssuerTLSConfig(env).HTTPSPort) {
					t.Fatal("issuer paths, listener and Service resolver disagree")
				}
				values := certIssuerObjectList(c["env"])
				if deploymentplan.Map(values[0])["name"] != "POSTGRES_PASSWORD" || deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(values[0])["valueFrom"])["secretKeyRef"])["name"] != b.IssuerRuntimeSecret || !strings.Contains(deploymentplan.String(managedNamedField(values, "CERT_ISSUER_DB_DSN")["value"]), "$(POSTGRES_PASSWORD)") {
					t.Fatal("issuer database credentials were flattened or ordered after their consumer")
				}
				c["env"] = values[1:]
				// This missing candidate field caused the real rollout failure.
				for i, value := range certIssuerObjectList(c["env"]) {
					if deploymentplan.Map(value)["name"] == "CERT_ISSUER_CA_CERT_PATH" {
						values := certIssuerObjectList(c["env"])
						c["env"] = append(values[:i], values[i+1:]...)
						break
					}
				}
				if validateEnvironmentIssuerStartup(object) == nil {
					t.Fatal("incomplete rendered candidate passed startup qualification")
				}
			})
		}
	}
}

func TestIssuerObservationRequiresOriginalReferencedRuntimeSecret(t *testing.T) {
	for _, mode := range []string{"valid", "missing-key", "unreferenced", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			_, b, object := issuerRuntimeFixture(t, "dev")
			secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": b.IssuerRuntimeSecret, "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{"device-ca.crt": "fixture-cert", "client-ca.crt": "fixture-cert", "POSTGRES_PASSWORD": "private-marker"}}
			live := []deploymentplan.Object{secret}
			switch mode {
			case "missing-key":
				delete(deploymentplan.Map(secret["data"]), "device-ca.crt")
			case "unreferenced":
				deploymentplan.Map(secret["metadata"])["name"] = "other"
			case "ambiguous":
				other := deploymentplan.Clone(secret)
				deploymentplan.Map(other["metadata"])["name"] = "other"
				live = append(live, other)
				pod := environmentPKIPod(object)
				pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), deploymentplan.Object{"name": "other", "secret": deploymentplan.Object{"secretName": "other"}})
			}
			name, err := observeEnvironmentIssuerRuntimeSecret(object, live)
			if (err == nil) != (mode == "valid") || err == nil && name != b.IssuerRuntimeSecret || err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("issuer source was guessed, ambiguous or exposed credentials", err)
			}
		})
	}
}

func TestIssuerDerivationDoesNotEnableAnUnboundIssuanceDomain(t *testing.T) {
	for _, bound := range []bool{false, true} {
		env, b, object := issuerRuntimeFixture(t, "dev")
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
		if bound {
			for _, key := range []string{"CERT_ISSUER_MQTT_SERVER_PKI_ROOT_SHA256", "CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256"} {
				c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": key, "value": strings.Repeat("a", 64)})
			}
		}
		if err := applyEnvironmentIssuerRuntime(env, object, b); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"CERT_ISSUER_MQTT_SERVER_DNS_NAMES", "CERT_ISSUER_OPENBAO_HOST_DNS_NAMES"} {
			if (managedNamedField(c["env"], key) != nil) != bound {
				t.Fatal("a derived DNS name enabled a domain without its original root binding")
			}
		}
	}
}
