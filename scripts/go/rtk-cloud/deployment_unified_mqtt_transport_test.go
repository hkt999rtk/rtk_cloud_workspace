package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func coreMQTTFixture(t *testing.T, environment string) ([]deploymentplan.Definition, []deploymentplan.Object) {
	t.Helper()
	ns := "video-cloud-" + environment + "-video-cloud"
	root := testOpenBaoCAPEM(t)
	block, _ := pem.Decode(root)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pin := fmtMQTTRootPin(cert)
	app := deploymentplan.Object{"name": "app", "image": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64), "env": []any{deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", "value": pin}, deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_ADDR", "value": "mqtt." + ns + ".svc:1883"}}, "volumeMounts": []any{}}
	app["env"] = append(certIssuerObjectList(app["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE", "value": "/identity/current.json"}, deploymentplan.Object{"name": "VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256", "value": pin})
	pod := deploymentplan.Object{"containers": []any{app}, "volumes": []any{}}
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-api", "namespace": ns, "uid": "original-api", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-" + environment}}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": pod}}}
	live := []deploymentplan.Object{}
	for _, role := range []struct{ name, key, data string }{{"mqtt-pki-bundles", "roots.pem", string(root)}, {"mqtt-pki-crls", "crls.json", "[{\"issuer_id\":\"original\"}]"}, {"service-root", "root.pem", string(root)}, {"account-manager-crls", "crls.json", "[{\"state_path\":\"/identity/client-crl.json\"}]"}} {
		relative := "public/" + role.key
		pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), deploymentplan.Object{"name": role.name, "configMap": deploymentplan.Object{"name": role.name, "items": []any{deploymentplan.Object{"key": role.key, "path": relative}}}})
		app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), deploymentplan.Object{"name": role.name, "mountPath": "/mounted/" + role.name, "readOnly": true})
		live = append(live, deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": role.name, "namespace": ns, "uid": "original-" + role.name}, "data": deploymentplan.Object{role.key: role.data}})
	}
	pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), deploymentplan.Object{"name": "service-identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-client-identity"}})
	app["volumeMounts"] = append(certIssuerObjectList(app["volumeMounts"]), deploymentplan.Object{"name": "service-identity", "mountPath": "/identity"})
	defs := []deploymentplan.Definition{{Object: object}}
	for _, service := range []struct {
		name, portName string
		port           float64
	}{{"mqtt-pki", "mqtts", 8883}, {"pki-controller", "https", 18446}, {"certissuer", "https", 9443}} {
		defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": service.name, "namespace": ns}, "spec": deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": service.portName, "port": service.port, "targetPort": service.portName}}}}})
	}
	for _, role := range []struct{ name, key string }{{"pki-controller", "PKI_HOST_ROOT_SHA256"}, {"certissuer", "CERT_ISSUER_HOST_ROOT_SHA256"}} {
		defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": role.name, "namespace": ns}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "server", "env": []any{deploymentplan.Object{"name": role.key, "value": pin}}}}}}}}})
	}
	return defs, live
}
func fmtMQTTRootPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	const alphabet = "0123456789abcdef"
	b := make([]byte, 64)
	for i, v := range sum {
		b[i*2] = alphabet[v>>4]
		b[i*2+1] = alphabet[v&15]
	}
	return string(b)
}

func TestCoreMQTTTransportDerivesOnlyAffectedConsumer(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			defs, _ := coreMQTTFixture(t, environment)
			original := deploymentplan.Clone(defs[0].Object)
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
			other := deploymentplan.Clone(original)
			deploymentplan.Map(other["metadata"])["name"] = "video-cloud-logingester"
			defs = append(defs, deploymentplan.Definition{Object: other})
			untouched := deploymentplan.Clone(other)
			if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
				t.Fatal(err)
			}
			ns := "video-cloud-" + environment + "-video-cloud"
			if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ADDR") != "mqtt-pki."+ns+".svc:8883" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_TLS") != "true" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH") != "/mounted/mqtt-pki-bundles/public/roots.pem" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL") != "https://pki-controller."+ns+".svc:18446" {
				t.Fatal("derived transport mismatch")
			}
			if mqttFoundationEnv(c, "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_URL") != "https://certissuer."+ns+".svc:9443" || mqttFoundationEnv(c, "VIDEO_CLOUD_CONTROLLER_IDENTITY_TLS_CA") != "/mounted/service-root/public/root.pem" || mqttFoundationEnv(c, "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE") != "/identity/current.json" {
				t.Fatal("original managed identity or derived management trust lost")
			}
			if !reflect.DeepEqual(environmentPKIPod(original)["volumes"], environmentPKIPod(defs[0].Object)["volumes"]) || !reflect.DeepEqual(other, untouched) || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != mqttFoundationEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0]), "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") {
				t.Fatal("unrelated configuration, Root or original volume changed")
			}
			if err := validateEnvironmentCoreMQTTTransport(defs); err != nil {
				t.Fatal(err)
			}
			// Reproduce the failed deployment: Root/name remain, but TLS/CA disappear.
			var remaining []any
			for _, raw := range certIssuerObjectList(c["env"]) {
				if name := deploymentplan.Map(raw)["name"]; name != "VIDEO_CLOUD_MQTT_TLS" && name != "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH" {
					remaining = append(remaining, raw)
				}
			}
			c["env"] = remaining
			if err := validateEnvironmentCoreMQTTTransport(defs); err == nil {
				t.Fatal("original startup regression not caught before rollout")
			}
		})
	}
}

func TestCoreMQTTTransportRejectsUnsafeMappingAndManualInputs(t *testing.T) {
	for _, problem := range []string{"missing-service", "bad-port", "missing-mount", "writable", "traversal", "missing-item", "shadowed", "duplicate-env", "manual-path", "bad-root", "reference-root"} {
		t.Run(problem, func(t *testing.T) {
			defs, _ := coreMQTTFixture(t, "dev")
			pod := environmentPKIPod(defs[0].Object)
			c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			mount := deploymentplan.Map(certIssuerObjectList(c["volumeMounts"])[0])
			source := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])["configMap"])
			env := map[string]string{}
			switch problem {
			case "missing-service":
				defs = defs[:2]
			case "bad-port":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(defs[1].Object["spec"])["ports"])[0])["targetPort"] = "cleartext"
			case "missing-mount":
				c["volumeMounts"] = certIssuerObjectList(c["volumeMounts"])[1:]
			case "writable":
				mount["readOnly"] = false
			case "traversal":
				deploymentplan.Map(certIssuerObjectList(source["items"])[0])["path"] = "../roots.pem"
			case "missing-item":
				source["items"] = []any{}
			case "shadowed":
				c["volumeMounts"] = append(certIssuerObjectList(c["volumeMounts"]), deploymentplan.Object{"name": "other", "mountPath": "/mounted/mqtt-pki-bundles/public"})
			case "duplicate-env":
				c["env"] = append(certIssuerObjectList(c["env"]), certIssuerObjectList(c["env"])[0])
			case "manual-path":
				env["VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH"] = "/manual"
			case "reference-root":
				e := deploymentplan.Map(certIssuerObjectList(c["env"])[0])
				delete(e, "value")
				e["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "unobserved", "key": "pin"}}
			case "bad-root":
				deploymentplan.Map(certIssuerObjectList(c["env"])[0])["value"] = "bad-pin"
			}
			err := deriveEnvironmentCoreMQTTTransport(env, defs)
			var b *deploymentplan.Blocker
			if !errors.As(err, &b) || b.ID != "deployment.configuration.mqtt-transport" {
				t.Fatalf("unverified target accepted: %v", err)
			}
		})
	}
}

func TestCoreMQTTTransportChecksOriginalSourceOwnerAndIndependentPin(t *testing.T) {
	for _, problem := range []string{"", "owner", "root", "missing-key", "malformed-crl", "client-crl-outside-pvc", "client-crl-unprepared-parent", "missing-permission-init", "private-error"} {
		t.Run(problem, func(t *testing.T) {
			defs, live := coreMQTTFixture(t, "dev")
			if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
				t.Fatal(err)
			}
			if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
				t.Fatal(err)
			}
			if problem == "missing-permission-init" {
				delete(environmentPKIPod(defs[0].Object), "initContainers")
			}
			p := &deploymentplan.Plan{}
			for _, d := range defs {
				p.Resources = append(p.Resources, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Desired: d.Object})
			}
			for _, o := range live {
				p.Resources = append(p.Resources, deploymentplan.Resource{ID: deploymentplan.ID(o), Action: "retain", UID: deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"]), Desired: deploymentplan.Projection(o)})
			}
			read := func(_ context.Context, id string) (deploymentplan.Object, error) {
				if problem == "private-error" {
					return nil, errors.New("private-token-from-tool")
				}
				for _, o := range live {
					if deploymentplan.ID(o) == id {
						copy := deploymentplan.Clone(o)
						switch problem {
						case "owner":
							deploymentplan.Map(copy["metadata"])["uid"] = "foreign"
						case "root":
							if strings.Contains(id, "bundles") {
								deploymentplan.Map(copy["data"])["roots.pem"] = string(testOpenBaoCAPEM(t))
							}
						case "missing-key":
							copy["data"] = deploymentplan.Object{}
						case "malformed-crl":
							if strings.Contains(id, "crls") {
								deploymentplan.Map(copy["data"])["crls.json"] = "invalid"
							}
						case "client-crl-outside-pvc":
							if strings.Contains(id, "account-manager-crls") {
								deploymentplan.Map(copy["data"])["crls.json"] = `[{"state_path":"/other/client-crl.json"}]`
							}
						case "client-crl-unprepared-parent":
							if strings.Contains(id, "account-manager-crls") {
								deploymentplan.Map(copy["data"])["crls.json"] = `[{"state_path":"/identity/unprepared/client-crl.json"}]`
							}
						}
						return copy, nil
					}
				}
				return nil, errors.New("missing")
			}
			err := checkEnvironmentCoreMQTTTransport(context.Background(), p, identityPermissionAlpine, read)
			if problem == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("unsafe evidence not safely blocked: %v", err)
			}
		})
	}
}

func TestCoreMQTTTransportAdoptionRemovesDerivedInputsAndPreservesRoot(t *testing.T) {
	defs, live := coreMQTTFixture(t, "dev")
	if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
		t.Fatal(err)
	}
	object := defs[0].Object
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
	image := deploymentplan.String(c["image"])
	release := environmentRelease{Environment: "dev", Stack: "video-cloud-dev", Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": {Reference: image}}}
	bindings, err := observeEnvironmentBindings("dev", release, append(live, object))
	if err != nil {
		t.Fatal(err)
	}
	fields := bindings.Pods[0].Containers[0].Fields
	for _, key := range []string{"VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE", "VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256"} {
		if mqttFoundationEnv(fields, key) != mqttFoundationEnv(c, key) || mqttFoundationEnv(fields, key) == "" {
			t.Fatal("original managed Service identity binding lost during observation", key)
		}
	}
	if mqttFoundationEnv(fields, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") {
		t.Fatal("independent Root lost")
	}
	for _, raw := range certIssuerObjectList(fields["env"]) {
		if derivedCoreMQTTSetting(deploymentplan.String(deploymentplan.Map(raw)["name"])) {
			t.Fatal("derived input imported as manual state")
		}
	}
	fields["env"] = append(certIssuerObjectList(fields["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH", "value": "/manual"})
	if err := validateEnvironmentIdentityBindings(bindings, "dev"); err == nil {
		t.Fatal("manual derived binding accepted")
	}
}

func TestCoreMQTTTransportTransitionPreservesTrustAndMounts(t *testing.T) {
	defs, _ := coreMQTTFixture(t, "dev")
	original := deploymentplan.Clone(defs[0].Object)
	if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironmentPKITransition(defs, []deploymentplan.Object{original}); err != nil {
		t.Fatal("safe derived transport blocked", err)
	}
	changed := deploymentplan.Clone(defs[0].Object)
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed)["containers"])[0])
	for _, raw := range certIssuerObjectList(c["env"]) {
		if e := deploymentplan.Map(raw); e["name"] == "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256" {
			e["value"] = strings.Repeat("b", 64)
		}
	}
	copyDefs := append([]deploymentplan.Definition(nil), defs...)
	copyDefs[0].Object = changed
	if err := validateEnvironmentPKITransition(copyDefs, []deploymentplan.Object{original}); err == nil {
		t.Fatal("derived normalization hid Root replacement")
	}
	changed = deploymentplan.Clone(defs[0].Object)
	deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed)["volumes"])[0])["configMap"])["name"] = "foreign-trust"
	copyDefs[0].Object = changed
	if err := validateEnvironmentPKITransition(copyDefs, []deploymentplan.Object{original}); err == nil {
		t.Fatal("derived normalization hid trust source replacement")
	}
}

func TestCoreMQTTTransportDoesNotEnableUnadoptedProfile(t *testing.T) {
	defs, _ := coreMQTTFixture(t, "dev")
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
	c["env"] = []any{deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_ADDR", "value": "mqtt:1883"}}
	original := deploymentplan.Clone(defs[0].Object)
	if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, defs[0].Object) {
		t.Fatal("PKI enabled without original Root")
	}
	c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME", "value": "broker.invalid"})
	if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err == nil {
		t.Fatal("unbound PKI accepted")
	}
}
