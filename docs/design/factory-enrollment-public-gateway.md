# Public factory enrollment gateway

The factory enrollment service is shared across Products. The production-run JWT selects one Cloud and Product, and the signing request uses that Product's issuer. The external factory hostname is independent of the Admin Console and device runtime hostnames. Cloud Test Lab is a simplified development test flow and must not be used for mass production.

```mermaid
sequenceDiagram
    autonumber
    participant O as Platform operator
    participant C as Cloud owner
    participant F as Factory gateway
    participant D as Device
    participant G as Factory mTLS ingress
    participant E as Factory enrollment service
    participant A as Account Manager
    participant I as Product issuer
    F->>O: Factory CSR (private key stays at factory)
    O-->>F: Factory client certificate for Cloud + factory ID
    C->>A: Create Product production run (quantity and expiry)
    A-->>C: One-time production-run JWT
    C-->>F: Deliver JWT securely
    D->>D: Generate and retain device private key
    D->>F: Device CSR and devid
    F->>G: POST /v1/factory/enroll with mTLS + JWT + CSR
    G->>E: Verified client certificate identity + request
    E->>E: Match certificate Cloud/factory to JWT claims
    E->>A: Reserve run quota and verify current authority
    E->>I: Sign CSR with selected Product issuer
    I-->>E: Device certificate and chain
    E-->>F: Certificate bundle
    F-->>D: Install certificate and chain; retain device key
```

## Deployment configuration

- `FACTORY_ENROLL_DOMAIN` defaults to `factory-enroll.<CLOUD_STACK_NAME>.<CLOUD_DNS_ROOT_DOMAIN>` and may be overridden with an independent hostname under the managed DNS zone.
- `FACTORY_ENROLL_PUBLIC_ENABLED=true` adds the DNS record, HTTPS certificate hostname and a dedicated mTLS Ingress. The flag remains off until the affected services, factory CA/CRL and dev acceptance checks are ready. Dev is enabled first; staging and production require separate qualification.
- Only the exact external path `POST /v1/factory/enroll` is published. The Ingress forwards to the service's `/v1/factory/public-enroll` handler, which requires production JWT authentication and a matching factory certificate identity. Internal health, recovery and test paths stay private.
- The platform operator keeps the factory CA key in the environment-local SecretStore. The Ingress receives only the CA certificate and its CRL. Revocation requires publishing the updated CRL and verifying rejection at the public hostname.

## Acceptance boundary

The public endpoint is ready only when DNS, server TLS, factory mTLS CA/CRL, Product PKI, Account Manager reservation and client certificate rejection have all been tested on dev. A healthy pod or an unauthenticated `401` does not establish that factory enrollment works.
