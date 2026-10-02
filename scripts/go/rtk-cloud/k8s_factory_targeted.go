package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"reflect"
)

// A public Factory image rollout reuses its existing issuer identity and runtime
// credentials. Bootstrap and identity reconciliation remain explicit operations.
func lkeApplyTargetedFactoryEnroll(paths provisionPaths, env map[string]string) error {
	if !activeCanonicalSecretStore {
		return errors.New("targeted Factory rollout requires the selected canonical SecretStore")
	}
	if err := lkeRequireBaselineIdentityDeployment(env, "video-cloud", "factoryenroll"); err != nil {
		return err
	}
	saved, err := readDeploymentTLSBundle(sensitiveEnvironmentPath(paths, "certissuer"), "Factory issuer client", []string{"factory.crt", "factory.key", "service-ca.crt"})
	if err != nil {
		return err
	}
	if saved == nil {
		return errors.New("targeted Factory rollout requires the existing canonical issuer client; bootstrap or reconcile explicitly")
	}
	if err := validateDeploymentTLS(saved["factory.crt"], saved["factory.key"], saved["service-ca.crt"], []string{"factoryenroll"}, nil, x509.ExtKeyUsageClientAuth); err != nil {
		return fmt.Errorf("existing Factory issuer client: %w", err)
	}
	client, err := lkeGetOptionalSecret(lkeNamespaceName(env, "video-cloud"), "factoryenroll-certissuer-client")
	if err != nil {
		return err
	}
	if client == nil {
		return errors.New("targeted Factory rollout requires the existing issuer client Secret")
	}
	for file, key := range map[string]string{"factory.crt": "client.crt", "factory.key": "client.key", "service-ca.crt": "ca.crt"} {
		live, err := kubernetesSecretBytes(client, key)
		// The initial renderer can use PEM without a final newline while
		// writeSensitiveFile persists that same key with one. Only terminal
		// CR/LF are formatting; preserve every live Secret byte during rollout.
		if err != nil || !bytes.Equal(bytes.TrimRight(live, "\r\n"), bytes.TrimRight([]byte(saved[file]), "\r\n")) {
			return fmt.Errorf("Factory issuer client %s differs from canonical identity; reconcile explicitly, no overwrite", key)
		}
	}
	runtime, err := lkeGetOptionalSecret(lkeNamespaceName(env, "video-cloud"), "factoryenroll-runtime")
	if err != nil {
		return err
	}
	if runtime == nil {
		return errors.New("targeted Factory rollout requires the existing runtime Secret")
	}
	for key, expected := range map[string]string{
		"FACTORY_ENROLL_AUTH_KEY":                lkeFactoryEnrollAuthKey(env),
		"FACTORY_ENROLL_PRODUCTION_JWT_SECRET":   lkeFactoryProductionJWTSecret(env),
		"FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE": lkeFactoryProductionJWTAudience(env),
		"FACTORY_ENROLL_ACCOUNT_MANAGER_TOKEN":   lkeRuntimeSecretValue("factory-admission"),
		"FACTORY_ENROLL_RECOVERY_TOKEN":          lkeHandoffRuntimeValue(env, lkeFactoryHandoffToken()),
		"POSTGRES_PASSWORD":                      lkeRuntimeSecretValue("postgres"),
	} {
		live, err := kubernetesSecretBytes(runtime, key)
		if key == "FACTORY_ENROLL_RECOVERY_TOKEN" && expected == "" {
			data, _ := runtime["data"].(map[string]any)
			encoded, present := data[key].(string)
			if present {
				live, err = base64.StdEncoding.DecodeString(encoded)
			}
		}
		if err != nil || (expected == "" && key != "FACTORY_ENROLL_RECOVERY_TOKEN") || !bytes.Equal(live, []byte(expected)) {
			return fmt.Errorf("Factory runtime %s differs from canonical settings; reconcile explicitly, no overwrite", key)
		}
	}
	for _, secret := range []map[string]any{client, runtime} {
		metadata, _ := secret["metadata"].(map[string]any)
		if uid, _ := metadata["uid"].(string); uid == "" {
			return errors.New("Factory Secret identity is missing; rollout refused")
		}
	}
	material := lkeCertIssuerMaterial{FactoryCert: saved["factory.crt"], FactoryKey: saved["factory.key"], ServiceCA: saved["service-ca.crt"]}
	if err := kubectlApply(lkeFactoryEnrollServiceManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeFactoryEnrollDeploymentManifest(env, material)); err != nil {
		return err
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/factoryenroll", "--timeout", firstNonEmpty(os.Getenv("LKE_FACTORYENROLL_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	for _, before := range []struct {
		name   string
		secret map[string]any
	}{{"factoryenroll-certissuer-client", client}, {"factoryenroll-runtime", runtime}} {
		after, err := lkeGetOptionalSecret(lkeNamespaceName(env, "video-cloud"), before.name)
		if err != nil {
			return err
		}
		metadata, _ := before.secret["metadata"].(map[string]any)
		afterMetadata, _ := after["metadata"].(map[string]any)
		if after == nil || metadata["uid"] != afterMetadata["uid"] || !reflect.DeepEqual(before.secret["data"], after["data"]) {
			return fmt.Errorf("Factory Secret %s changed during rollout; reconcile explicitly", before.name)
		}
	}
	return nil
}
