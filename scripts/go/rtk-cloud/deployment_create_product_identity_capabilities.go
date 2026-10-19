package main

import (
	"context"
	"os/exec"
	"slices"
	"strings"
)

type environmentProductCapability struct {
	Image, Binary, Service string
	Features               []string
}

func productRegistrationCapabilities(image string, outputs []environmentCreateProductIdentity) ([]environmentProductCapability, error) {
	binaries := map[string]string{"mqtt": "mqttfoundation", "shadow": "shadowworker", "ota": "otaservice", "video-storage": "videostorage", "webrtc": "webrtcservice", "logger": "cloudlogger"}
	var caps []environmentProductCapability
	seen := map[string]bool{}
	for _, p := range outputs {
		role := strings.TrimPrefix(p.Subject, "service:")
		binary := binaries[role]
		if binary == "" || seen[role] {
			return nil, environmentCreateBootstrapBlocker("product capability target is outside the exact selected lifecycle")
		}
		seen[role] = true
		caps = append(caps, environmentProductCapability{image, "/app/" + binary, binary, []string{"service-registration-bootstrap-ack-v1"}})
	}
	return caps, nil
}

// Qualification runs only the immutable release's metadata interface; no
// deployment configuration, network or private identity enters the container.
func verifyCreateNormalTailProductCapabilities(ctx context.Context, release environmentRelease, products *createNormalTailProductLifecycle, testLab bool) error {
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", args...).Output()
	}
	if err := verifyProductCapabilities(ctx, release, products, run); err != nil {
		return err
	}
	return verifyManagedTestLabCapability(ctx, release, testLab, run)
}

func managedMQTTRequiredCapabilities(testLab bool) []string {
	features := []string{"managed-callback-export-v1"}
	if testLab {
		features = append(features, "managed-test-lab-callback-v1")
	}
	return features
}

func verifyManagedTestLabCapability(ctx context.Context, release environmentRelease, enabled bool, run func(context.Context, ...string) ([]byte, error)) error {
	if !enabled {
		return ctx.Err()
	}
	image := release.Images["LKE_MQTT_PKI_IMAGE"].Reference
	if run == nil || !immutableEnvironmentImage.MatchString(image) {
		return environmentCreateBootstrapBlocker("enabled Test Lab requires its formal native MQTT image")
	}
	raw, err := run(ctx, "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/usr/local/bin/emqxpkihost", image, "--deployment-capabilities")
	var actual struct {
		Schema, Service string
		Features        []string
	}
	if err != nil || len(raw) > 16<<10 || decodeEnvironmentJSON(raw, &actual) != nil || actual.Schema != "rtk-cloud.deployment-capabilities/v1" || actual.Service != "emqxpkihost" {
		return environmentCreateBootstrapBlocker("enabled Test Lab has no verified native MQTT callback capability")
	}
	for _, feature := range managedMQTTRequiredCapabilities(true) {
		if !slices.Contains(actual.Features, feature) {
			return environmentCreateBootstrapBlocker("enabled Test Lab image lacks its registered native callback capability")
		}
	}
	return ctx.Err()
}

func verifyProductCapabilities(ctx context.Context, release environmentRelease, products *createNormalTailProductLifecycle, run func(context.Context, ...string) ([]byte, error)) error {
	if products == nil || len(products.Outputs) == 0 || run == nil {
		return environmentCreateBootstrapBlocker("qualified product lifecycle capabilities are unavailable")
	}
	image := release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	if !immutableEnvironmentImage.MatchString(image) {
		return environmentCreateBootstrapBlocker("product lifecycle requires its exact formal immutable API image")
	}
	type capability struct {
		binary, service string
		features        []string
	}
	caps := []capability{{"/app/certissuer", "certissuer", []string{"create-service-enrollment-v1", "registry-only-issuance-v1"}}, {"/app/pkimanagement", "pkimanagement", []string{"first-trust-v1", "deployment-bootstrap-finalize-v1"}}, {"/app/serviceidentity-bootstrap", "serviceidentity-bootstrap", []string{"owned-server-export-v1"}}}
	productCaps, err := productRegistrationCapabilities(image, products.Outputs)
	if err != nil {
		return err
	}
	for _, cap := range productCaps {
		caps = append(caps, capability{cap.Binary, cap.Service, cap.Features})
	}
	for _, cap := range caps {
		raw, err := run(ctx, "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", cap.binary, image, "--deployment-capabilities")
		var actual struct {
			Schema   string   `json:"schema"`
			Service  string   `json:"service"`
			Features []string `json:"features"`
		}
		if err != nil || len(raw) > 16<<10 || decodeEnvironmentJSON(raw, &actual) != nil || actual.Schema != "rtk-cloud.deployment-capabilities/v1" || actual.Service != cap.service {
			return environmentCreateBootstrapBlocker("formal product image has no verified native enrollment, registration ACK or finalization capability")
		}
		for _, feature := range cap.features {
			if !slices.Contains(actual.Features, feature) {
				return environmentCreateBootstrapBlocker("formal product image lacks a required native lifecycle capability")
			}
		}
	}
	account := release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference
	if !immutableEnvironmentImage.MatchString(account) {
		return environmentCreateBootstrapBlocker("product lifecycle requires its exact formal Account Manager image")
	}
	raw, err := run(ctx, "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/rtk-account-manager", account, "--deployment-capabilities")
	var actual struct {
		Schema   string   `json:"schema"`
		Service  string   `json:"service"`
		Features []string `json:"features"`
	}
	if err != nil || len(raw) > 16<<10 || decodeEnvironmentJSON(raw, &actual) != nil || actual.Schema != "rtk-cloud.deployment-capabilities/v1" || actual.Service != "account-manager" || !slices.Contains(actual.Features, "deployment-bootstrap-finalize-v1") {
		return environmentCreateBootstrapBlocker("formal Account Manager image lacks the native authenticated session finalizer")
	}
	// The offline same-Root signing and native policy producer are executable,
	// rather than inferred from a different binary's metadata response.
	_, err = run(ctx, "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", image, "-ec", "test -x /app/pkiceremony; test -x /app/pkicontroller; test -x /app/serviceidentity-bootstrap; openssl version >/dev/null")
	if err != nil {
		return environmentCreateBootstrapBlocker("formal product image lacks the native same-Root ceremony and policy producers")
	}
	return ctx.Err()
}
