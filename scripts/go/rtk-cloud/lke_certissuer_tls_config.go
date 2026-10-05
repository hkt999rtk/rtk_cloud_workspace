package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// CertIssuer terminates the caller's TLS connection itself. The public route,
// its internal endpoint and serving certificate share this resolved policy;
// ingress termination and forwarded-identity modes are not deployment options.
type lkeCertIssuerTLSConfig struct {
	Namespace  string
	PublicHost string
	HTTPSPort  int
}

const lkeCertIssuerHTTPSPort = 9443

func lkeResolveCertIssuerTLSConfig(env map[string]string) lkeCertIssuerTLSConfig {
	return lkeCertIssuerTLSConfig{
		Namespace:  lkeNamespaceName(env, "video-cloud"),
		PublicHost: strings.TrimSpace(env["VIDEO_CLOUD_CERTISSUER_DOMAIN"]),
		HTTPSPort:  lkeCertIssuerHTTPSPort,
	}
}

func (config lkeCertIssuerTLSConfig) Validate() error {
	if len(config.Namespace) > 63 || strings.Contains(config.Namespace, ".") {
		return fmt.Errorf("invalid CertIssuer namespace %q: use a DNS label of 1 to 63 characters", config.Namespace)
	}
	if err := validateManagedDNSHostname(config.Namespace); err != nil {
		return fmt.Errorf("invalid CertIssuer namespace: %w", err)
	}
	if config.PublicHost != "" {
		if err := validateManagedDNSHostname(config.PublicHost); err != nil {
			return fmt.Errorf("invalid CertIssuer public host: %w", err)
		}
	}
	return nil
}

func (config lkeCertIssuerTLSConfig) ServerDNSNames() []string {
	names := []string{
		"certissuer",
		"certissuer." + config.Namespace,
		"certissuer." + config.Namespace + ".svc",
		"certissuer." + config.Namespace + ".svc.cluster.local",
	}
	if config.PublicHost != "" && !slices.Contains(names, config.PublicHost) {
		names = append(names, config.PublicHost)
	}
	return names
}

func (config lkeCertIssuerTLSConfig) InternalBaseURL() string {
	return fmt.Sprintf("https://certissuer.%s.svc.cluster.local:%d", config.Namespace, config.HTTPSPort)
}

func (config lkeCertIssuerTLSConfig) PublicRoute() lkePublicHTTPSRoute {
	return lkePublicHTTPSRoute{Host: config.PublicHost, Namespace: config.Namespace, Service: "certissuer", ServicePort: config.HTTPSPort, TargetPort: config.HTTPSPort, Protocol: "HTTPS"}
}

func (config lkeCertIssuerTLSConfig) IngressNginxHelmValue() string {
	return "controller.extraArgs.enable-ssl-passthrough=" + strconv.FormatBool(config.PublicHost != "")
}
