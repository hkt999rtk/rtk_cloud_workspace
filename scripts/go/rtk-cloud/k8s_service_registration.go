package main

import (
	"encoding/base64"
	"fmt"
)

const serviceRegistrationTLSSecretName = "account-manager-service-registration-tls"

var serviceRegistrationTLSSecretKeys = []string{"tls.crt", "tls.key", "client-ca.crt", "client.crl"}

// Refuse to roll Account Manager with missing or invalid listener material.
// The matching plugin identity checks also verify the mutual trust boundary.
func lkeRequireServiceRegistrationSecret(env map[string]string) error {
	secret, err := kubectlResourceJSON(lkeNamespaceName(env, "account-manager"), "secret", serviceRegistrationTLSSecretName)
	if err != nil {
		return fmt.Errorf("service registration TLS Secret is unavailable: %w", err)
	}
	if err := validateServiceRegistrationSecretData(secret); err != nil {
		return err
	}
	return validateRegistrationServerMaterial(secret, lkeRegistrationServerDNS(env))
}

func lkeRegistrationServerDNS(env map[string]string) string {
	return "account-manager." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local"
}

func validateServiceRegistrationSecretData(secret map[string]any) error {
	return validateRequiredKubernetesSecretData(secret, "service registration TLS Secret", serviceRegistrationTLSSecretKeys)
}

func validateRequiredKubernetesSecretData(secret map[string]any, label string, keys []string) error {
	data, ok := secret["data"].(map[string]any)
	if !ok {
		return fmt.Errorf("%s has no data", label)
	}
	for _, key := range keys {
		encoded, ok := data[key].(string)
		if !ok || encoded == "" {
			return fmt.Errorf("%s lacks %s", label, key)
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			return fmt.Errorf("%s has invalid %s", label, key)
		}
	}
	return nil
}
