package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type certIssuerIngressList struct {
	Items []struct {
		Metadata struct {
			Namespace   string            `json:"namespace"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			IngressClassName string `json:"ingressClassName"`
			Rules            []struct {
				Host string `json:"host"`
				HTTP struct {
					Paths []struct {
						Backend struct {
							Service struct {
								Name string `json:"name"`
							} `json:"service"`
						} `json:"backend"`
					} `json:"paths"`
				} `json:"http"`
			} `json:"rules"`
		} `json:"spec"`
	} `json:"items"`
}

func certIssuerDirectMTLSSettings(deployments liveDeploymentList) map[string]string {
	for _, deployment := range deployments.Items {
		if deployment.Metadata.Name != "certissuer" {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "certissuer" {
				continue
			}
			settings := map[string]string{}
			for _, setting := range container.Env {
				settings[setting.Name] = setting.Value
			}
			// This capability explicitly requires the original caller's direct mTLS.
			if settings["CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED"] == "true" {
				return settings
			}
		}
	}
	return nil
}

// Public routing is separate from the internal managed App socket probe. This
// structural check does not claim a successful authenticated public request.
func verifyCertIssuerPublicIngress(kubeconfig, stack string, deployments liveDeploymentList, runtime *deploymentCheckRuntime) error {
	settings := certIssuerDirectMTLSSettings(deployments)
	if settings == nil {
		return nil
	}
	raw, err := secretCheckKubectl([]*deploymentCheckRuntime{runtime}, false, "--kubeconfig", kubeconfig, "get", "ingresses", "--all-namespaces", "-o", "json")
	if err != nil {
		return secretCheckFailure(err, "cannot inspect public CertIssuer ingress")
	}
	var ingresses certIssuerIngressList
	if json.Unmarshal(raw, &ingresses) != nil {
		return errors.New("public CertIssuer ingress metadata is invalid")
	}
	return validateCertIssuerPublicIngress(stack, settings, ingresses)
}

func validateCertIssuerPublicIngress(stack string, settings map[string]string, ingresses certIssuerIngressList) error {
	for _, ingress := range ingresses.Items {
		if !strings.HasPrefix(ingress.Metadata.Namespace, stack+"-") || ingress.Spec.IngressClassName != "nginx" {
			continue
		}
		for _, rule := range ingress.Spec.Rules {
			for _, path := range rule.HTTP.Paths {
				backend := path.Backend.Service.Name
				if backend != "certissuer" && backend != "public-certissuer-video-cloud" {
					continue
				}
				if ingress.Metadata.Annotations["nginx.ingress.kubernetes.io/ssl-passthrough"] != "true" {
					return errors.New("public CertIssuer ingress terminates caller TLS; direct Service client mTLS requires SSL passthrough")
				}
				// The controller's TCP passthrough targets a Service ClusterIP. The
				// HTTP ExternalName bridge cannot carry this authenticated path.
				if ingress.Metadata.Namespace != stack+"-video-cloud" || backend != "certissuer" {
					return errors.New("public CertIssuer passthrough must target the certissuer Service in its own namespace, not an ExternalName bridge")
				}
				if settings["CERT_ISSUER_TRUSTED_HEADER_ENABLED"] == "true" {
					return errors.New("public CertIssuer direct mTLS must not trust forwarded identity headers")
				}
				if settings["CERT_ISSUER_HOST_IDENTITY_STATE"] != "" && (rule.Host == "" || !slices.Contains(strings.Split(settings["CERT_ISSUER_HOST_DNS_NAMES"], ","), rule.Host)) {
					return fmt.Errorf("public CertIssuer managed serving DNS policy does not cover ingress host %s; qualify an approved issuer and serving certificate before routing", rule.Host)
				}
			}
		}
	}
	return nil
}
