# LKE External HAProxy Edge

Status: implemented for LKE staging in PR #181. This document is the current
deployment contract plus the failover-ready shape for future multi-VM edge
work.

The public LKE edge target is an external HAProxy VM, not a Linode
NodeBalancer. HAProxy runs directly on the VM OS as a systemd service and does
not use Docker. It only performs layer-4 TCP passthrough; TLS, mTLS, SNI,
Ingress routing, and application auth stay inside Kubernetes.

## Topology

```text
Internet
  -> HAProxy edge VM(s), TCP mode
  -> LKE node private IP:NodePort
  -> ingress-nginx / EMQX
  -> Kubernetes pod
```

The current implementation supports one HAProxy VM operationally. The config
and artifacts use array-shaped data for edge VMs so active-active DNS,
active-standby failover, or external DNS health checks can be added without
changing the public contract.

## Public Edge Contract

- `LKE_PUBLIC_EDGE_MODE=external-haproxy` is the only supported public edge
  mode for new LKE staging and production-like deployments.
- Linode NodeBalancer is a legacy design reference only and must not be used as
  the target public edge path.
- Public ports for v1 are `443/TCP` and `8883/TCP`.
- DNS A records point at HAProxy edge VM public IPs.
- `443/TCP` forwards to the ingress-nginx NodePort. Staging default:
  `30443`.
- `8883/TCP` uses HAProxy `balance roundrobin` and forwards to the EMQX/MQTT
  NodePort on each LKE node. Staging default: `31883`.
- MQTT defaults to a three-pod EMQX StatefulSet cluster with stable
  `mqtt-0..2` pod DNS and required pod anti-affinity, so one EMQX pod lands on
  each of the three staging nodes and HAProxy can round-robin the MQTTS backend
  across all nodes.
- PROXY protocol is off by default. Enable it only with
  `LKE_EDGE_HAPROXY_ENABLE_PROXY_PROTOCOL=true` after the Kubernetes backend is
  explicitly configured to accept it.

## HAProxy VM Runtime

Install HAProxy on the host with system packages and manage it with systemd.
Do not run HAProxy in Docker for this edge role.

Required host components:

- `haproxy`
- `prometheus-node-exporter`
- optional HAProxy exporter, bound only to private or localhost access
- systemd service override for file descriptor limits
- sysctl profile for large concurrent connection counts

Default high-concurrency settings:

- HAProxy `global maxconn 400000`, override with `LKE_EDGE_HAPROXY_MAXCONN`
- systemd `LimitNOFILE=1048576`
- tune `net.core.somaxconn`
- tune `net.core.netdev_max_backlog`
- tune `net.ipv4.ip_local_port_range`
- tune `net.ipv4.tcp_tw_reuse`
- tune `net.ipv4.tcp_fin_timeout`
- tune `net.ipv4.tcp_keepalive_time`
- tune `net.ipv4.tcp_keepalive_intvl`
- tune `net.ipv4.tcp_keepalive_probes`
- tune `net.netfilter.nf_conntrack_max` where conntrack is active

Validate every update with `haproxy -c -f /etc/haproxy/haproxy.cfg` before
graceful reload. HAProxy stats and exporter ports must not be public.

## Implemented Staging State

The live staging deployment validated on 2026-06-18 uses:

- HAProxy edge VM label: `video-cloud-staging-edge-haproxy-01`
- HAProxy edge VM public IP: `172.232.190.230`
- HAProxy edge VM private IP: `192.168.136.46`
- ingress-nginx Service: `NodePort` `443:30443/TCP`
- MQTT public Service: `NodePort` `8883:31883/TCP`
- MQTT StatefulSet: 3 clustered EMQX pods, spread one per LKE node.
- DNS A records for staging public hostnames point to the HAProxy edge VM.
- Full `scripts/run-staging-e2e.sh --confirm video-cloud-staging` passed with
  the default `10` users and `100` devices acceptance profile.

The live report for that run is:

```text
cloud_env/staging/lke/artifacts/staging-e2e/20260618T085249Z/test_report.md
```

## Artifacts

The deployer writes these redacted artifacts under the environment artifact
directory:

- `edge-haproxy/edge-vms.json`
- `edge-haproxy/upstreams.json`
- `edge-haproxy/haproxy.cfg`
- `edge-haproxy/install.sh`
- `edge-haproxy/validation.json`

`edge-vms.json` uses `edge_vms: []` even when there is only one VM. DNS update
logic accepts the multi-VM shape, but v1 writes one A record target because
failover automation is intentionally deferred.

`edge-vms.json` and `validation.json` also include `ssh_access` with the
operator SSH user and local key paths, for example `root` and
`~/.ssh/id_ed25519_rtkcloud`. These artifacts record paths only; they must not
contain private key material.

## Security

- Public firewall allows only required public ports, initially `443/TCP` and
  `8883/TCP`.
- SSH is restricted to operator CIDRs.
- K8s node NodePorts should accept traffic only from HAProxy edge private IPs
  where Linode firewall or VPC controls support it.
- HAProxy config must not contain application secrets, private keys, kubeconfig
  material, or API tokens.
- Public `80/TCP` remains closed; TLS issuance uses DNS-01.

## Validation

Provisioning validation records or should be checked with:

- `haproxy -vv`
- `haproxy -c -f /etc/haproxy/haproxy.cfg`
- `systemctl status haproxy`
- systemd `LimitNOFILE` for `haproxy.service`
- effective sysctl values
- `ss -s`
- HAProxy current sessions and max sessions
- HAProxy process RSS and file descriptor usage
- HTTPS `/healthz` or `/version` smoke through HAProxy
- MQTTS smoke through HAProxy

For the validated staging deployment, `haproxy -c` returned valid config,
`systemctl is-active haproxy` returned `active`, systemd reported
`LimitNOFILE=1048576`, and HAProxy listened on public `443` and `8883`.

Load testing should start with `maxconn 400000` for 100K MQTT plus API/WebRTC
traffic and measure memory, file descriptors, CPU, socket summary, and
connection saturation before changing the default. A 100K MQTTS run through TCP
proxying can approach 200K HAProxy-side sockets before API `/request_token` and
WebRTC signaling traffic are counted, so 200K is not enough headroom for the
100K-with-video sizing profile.

## Rollback

There is no NodeBalancer fallback path in the target deployment. First-version
rollback is manual: point DNS back to the previous known-good edge endpoint or
pause DNS updates while the HAProxy VM is repaired. Automated failover is a
future design item.
