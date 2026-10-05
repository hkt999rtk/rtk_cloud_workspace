package main

import "fmt"

func lkeCertIssuerPassthroughRoute(env map[string]string, route lkePublicHTTPSRoute) bool {
	config := lkeResolveCertIssuerTLSConfig(env)
	return route.Host != "" && route.Host == config.PublicHost && route.Namespace == config.Namespace && route.Service == "certissuer"
}

// CertIssuer owns both TLS identity and client authentication. A ClusterIP
// backend in the workload namespace preserves the caller's original handshake.
func lkeCertIssuerPassthroughIngressManifest(env map[string]string, route lkePublicHTTPSRoute) string {
	// Only the resolved CertIssuer route determines the TLS backend. General
	// route inputs must not select a different port or termination mode.
	route = lkeResolveCertIssuerTLSConfig(env).PublicRoute()
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: certissuer-public-mtls
  namespace: %s
  labels:
    app.kubernetes.io/name: certissuer-public-mtls
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
  annotations:
    nginx.ingress.kubernetes.io/ssl-passthrough: "true"
    nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - %s
  rules:
    - host: %s
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: certissuer
                port:
                  number: %d
`, route.Namespace, env["CLOUD_STACK_NAME"], route.Host, route.Host, route.ServicePort)
}
