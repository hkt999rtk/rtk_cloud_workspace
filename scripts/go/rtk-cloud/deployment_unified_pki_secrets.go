package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func secretIdentityBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.secret-identity", Resource: resource, Cause: cause, Next: "restore recorded independent server identity pins and original mounted Secret owners through managed lifecycle; validate key/chain/SAN/receipt and the reviewed validity window using the qualified native inspector, then repeat full preflight; never infer the trusted Root from the Secret or generate replacement credentials to pass"}
}

func validateSecretServerBindings(bindings []environmentSecretServerBinding, stack string) error {
	seen := map[string]bool{}
	for _, binding := range bindings {
		parts := strings.Split(binding.Workload, "/")
		id := binding.Workload + "/" + binding.Container + "/" + binding.CertificatePath
		if len(parts) != 3 || !keySet("Deployment", "StatefulSet", "DaemonSet")[parts[0]] || !strings.HasPrefix(parts[1], stack+"-") || parts[2] == "" || binding.Container == "" || seen[id] || !keySet("service", "mqtt", "openbao_tls")[binding.Domain] || binding.Subject == "" || !deploymentImageDigestPattern.MatchString("sha256:"+binding.Root) || len(binding.DNSNames) == 0 || binding.CertificatePath == binding.KeyPath {
			return errors.New("deployment.bindings: exact independent Secret server identity references required")
		}
		for _, path := range []string{binding.CertificatePath, binding.KeyPath} {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return errors.New("deployment.bindings: canonical Secret server file paths required")
			}
		}
		for _, name := range binding.DNSNames {
			if name == "" || strings.ContainsAny(name, ",\r\n") {
				return errors.New("deployment.bindings: explicit server DNS policy required")
			}
		}
		seen[id] = true
	}
	return nil
}

func (b environmentKubernetesBackend) CheckSecretServerIdentities(ctx context.Context, plan *deploymentplan.Plan, bindings []environmentSecretServerBinding, window int64) error {
	if plan == nil || validateSecretServerBindings(bindings, plan.Stack) != nil {
		return secretIdentityBlocker("", "Secret server identity bindings are invalid")
	}
	if window <= 0 {
		return validityWindowBlocker()
	}
	checked := map[int]bool{}
	err := b.inspectOriginalManagedStatePods(ctx, plan, secretIdentityBlocker, false, func(object, container deploymentplan.Object) ([]originalManagedStateProbe, error) {
		probes := []originalManagedStateProbe{}
		settings, err := environmentPKINamedFields(container["env"])
		if err != nil {
			return nil, err
		}
		certSetting, keySetting, hostPrefix, database := environmentSecretServerContract(object)
		if certSetting == "" {
			return probes, nil
		}
		// A managed host's current key is its durable state; its old bootstrap Secret
		// is not the active listener credential after enrollment.
		if hostPrefix != "" && deploymentplan.String(settings[hostPrefix+"_HOST_IDENTITY_STATE"]["value"]) != "" {
			return probes, nil
		}
		certPath, keyPath := deploymentplan.String(settings[certSetting]["value"]), deploymentplan.String(settings[keySetting]["value"])
		if certPath == "" && keyPath == "" {
			return probes, nil
		}
		match := -1
		for index, binding := range bindings {
			if binding.Workload == deploymentplan.ID(object) && binding.Container == container["name"] && binding.CertificatePath == certPath && binding.KeyPath == keyPath {
				if match >= 0 {
					return nil, secretIdentityBlocker(deploymentplan.ID(object), "Secret server identity references are ambiguous")
				}
				match = index
			}
		}
		if match < 0 {
			return nil, secretIdentityBlocker(deploymentplan.ID(object), "active Secret-backed server has no recorded independent identity policy")
		}
		binding := bindings[match]
		cert, certRef, certFence, err := b.bindOriginalPublicManifest(ctx, plan, object, container, certPath)
		if err != nil || certRef.Kind != "Secret" {
			return nil, secretIdentityBlocker(deploymentplan.ID(object), "original certificate Secret owner/projection is unavailable")
		}
		key, keyRef, keyFence, err := b.bindOriginalPublicManifest(ctx, plan, object, container, keyPath)
		if err != nil || keyRef.Kind != "Secret" {
			return nil, secretIdentityBlocker(deploymentplan.ID(object), "original key Secret owner/projection is unavailable")
		}
		probes = append(probes, originalManagedStateProbe{Fence: func(ctx context.Context) error {
			if err := certFence(ctx); err != nil {
				return err
			}
			return keyFence(ctx)
		}, CheckPod: func(pod deploymentplan.Object) error {
			actual := deploymentplan.Object(nil)
			for _, item := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"]) {
				if deploymentplan.Map(item)["name"] == container["name"] {
					actual = deploymentplan.Map(item)
				}
			}
			for path, expected := range map[string]environmentPublicFileRef{certPath: certRef, keyPath: keyRef} {
				got, err := environmentPublicFileReference(deploymentplan.Map(pod["spec"]), actual, path)
				if err != nil || got != expected {
					return errors.New("original Secret projection differs from planned owner")
				}
			}
			return nil
		}, Inspect: func(ctx context.Context, args []string) error {
			args = append(args, "inspect-secret-server", plan.Environment, binding.Domain, binding.Subject, binding.Root, strings.Join(binding.DNSNames, ","), certPath, keyPath, strconv.FormatInt(window, 10), database)
			raw, err := b.command(ctx, nil, args...)
			var report struct {
				Subject     string `json:"subject"`
				Fingerprint string `json:"fingerprint"`
				PublicKey   string `json:"public_key_sha256"`
				Root        string `json:"root_sha256"`
				Certificate string `json:"certificate_file_sha256"`
				Key         string `json:"key_file_sha256"`
				Registry    bool   `json:"registry_verified"`
				Window      int64  `json:"minimum_validity_seconds"`
			}
			certHash, keyHash := sha256.Sum256(cert), sha256.Sum256(key)
			if err != nil || decodeEnvironmentJSON(raw, &report) != nil || report.Subject != binding.Subject || report.Root != binding.Root || !deploymentImageDigestPattern.MatchString("sha256:"+report.Fingerprint) || !deploymentImageDigestPattern.MatchString("sha256:"+report.PublicKey) || report.Certificate != hex.EncodeToString(certHash[:]) || report.Key != hex.EncodeToString(keyHash[:]) || !report.Registry || report.Window != window {
				return errors.New("original Secret identity qualification incomplete")
			}
			checked[match] = true
			return nil
		}})
		return probes, nil
	})
	if err != nil {
		return err
	}
	for index := range bindings {
		if !checked[index] {
			return secretIdentityBlocker(bindings[index].Workload, "recorded Secret server identity has no qualified original active consumer")
		}
	}
	return nil
}

func environmentSecretServerContract(object deploymentplan.Object) (string, string, string, string) {
	name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
	switch name {
	case "certissuer", "certissuer-service-pki":
		return "CERT_ISSUER_SERVER_CERT", "CERT_ISSUER_SERVER_KEY", "CERT_ISSUER", "CERT_ISSUER_DB_DSN"
	case "pki-controller":
		return "PKI_SERVER_CERT", "PKI_SERVER_KEY", "PKI", "PKI_DATABASE_URL"
	case "video-cloud-api-pki":
		return "VIDEO_CLOUD_API_TLS_CERT_PATH", "VIDEO_CLOUD_API_TLS_KEY_PATH", "", "VIDEO_CLOUD_DB_DSN"
	}
	return "", "", "", ""
}
