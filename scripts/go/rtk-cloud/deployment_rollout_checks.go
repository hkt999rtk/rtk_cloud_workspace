package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// These checks extend credentials-check; they do not rotate credentials or deploy.
type rolloutTLSOptions struct {
	cert, key, ca, hostname, purpose string
	minDays                          int
}

func (c deploymentCredentialChecker) checkStorageReadOnly(store provisionObjectStore, prefix, name string) deploymentCredentialCheck {
	query := url.Values{"list-type": {"2"}, "max-keys": {"1"}, "prefix": {strings.Trim(prefix, "/") + "/"}}
	body, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", query, nil)
	if err != nil {
		return deploymentCredentialCheck{Name: name, Detail: "signed bucket listing failed; verify selected bucket and scoped credential"}
	}
	var listing provisionListBucketResult
	if xml.Unmarshal(body, &listing) != nil {
		return deploymentCredentialCheck{Name: name, Detail: "bucket list response is invalid XML"}
	}
	return deploymentCredentialCheck{Name: name, Passed: true, Detail: "inventory/key scope and signed list verified; writes not tested; no receipt created"}
}

var rolloutImagePattern = regexp.MustCompile(`^ghcr\.io/([a-z0-9_.-]+(?:/[a-z0-9_.-]+)+)@sha256:[a-f0-9]{64}$`)

func (c deploymentCredentialChecker) checkRolloutImage(values map[string]string, image string) deploymentCredentialCheck {
	check := deploymentCredentialCheck{Name: "GHCR release image"}
	match := rolloutImagePattern.FindStringSubmatch(image)
	if match == nil {
		check.Detail = "--image requires a reviewed GHCR @sha256 reference"
		return check
	}
	check.Name += " " + image
	username, token := values["GHCR_PULL_USERNAME"], values["GHCR_PULL_TOKEN"]
	if username == "" || token == "" {
		check.Detail = "GHCR_PULL_USERNAME and GHCR_PULL_TOKEN are required"
		return check
	}
	if strings.ContainsAny(username+token, "\r\n") {
		check.Detail = "GHCR credential contains CR/LF; normalize canonical files before copying into Secrets"
		return check
	}
	// This explicit exchange cannot borrow credentials from a Docker helper/keychain.
	registryToken, err := c.exchangeGHCRToken(username, token, match[1])
	if err != nil {
		check.Detail = "selected credential cannot obtain package pull access; verify token validity and read:packages"
		return check
	}
	manifestURL := strings.TrimRight(c.ghcrRegistryRoot, "/") + "/v2/" + match[1] + "/manifests/" + strings.SplitN(image, "@", 2)[1]
	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, manifestURL, nil)
	if err != nil {
		check.Detail = "cannot create registry manifest request"
		return check
	}
	req.Header.Set("Authorization", "Bearer "+registryToken)
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	if _, err := c.request(req); err != nil {
		check.Detail = "selected credential cannot read the exact image manifest; verify digest and package access"
		return check
	}
	if err := pullRolloutImage(username, token, image); err != nil {
		check.Detail = err.Error()
		return check
	}
	check.Passed, check.Detail = true, "selected credential and full linux/amd64 digest pull verified (local image cache may be used)"
	return check
}

func pullRolloutImage(username, token, image string) error {
	dir, err := os.MkdirTemp("", "rtk-rollout-pull-")
	if err != nil {
		return errors.New("cannot create private Docker credential directory")
	}
	defer os.RemoveAll(dir)
	encoded, _ := json.Marshal(map[string]any{
		"auths":      map[string]any{"ghcr.io": map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(username + ":" + token))}},
		"credsStore": "", "credHelpers": map[string]string{},
	})
	if os.WriteFile(filepath.Join(dir, "config.json"), encoded, 0o600) != nil {
		return errors.New("cannot write private Docker credential configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "--config", dir, "pull", "--platform", "linux/amd64", image)
	// Never return raw tool output: registry/helper diagnostics can contain secrets.
	if err := cmd.Run(); err != nil {
		return errors.New("linux/amd64 pull failed or timed out; check Docker daemon, digest availability and package access")
	}
	return nil
}

func checkRolloutTLS(options rolloutTLSOptions) deploymentCredentialCheck {
	check := deploymentCredentialCheck{Name: "rollout TLS"}
	info, err := os.Stat(options.key)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		check.Detail = "private key missing/nonregular or insecure permissions; require mode 0600"
		return check
	}
	identity, err := tls.LoadX509KeyPair(options.cert, options.key)
	if err != nil {
		check.Detail = "certificate/key unreadable, invalid or mismatched"
		return check
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		check.Detail = "invalid leaf certificate"
		return check
	}
	ca, err := os.ReadFile(options.ca)
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		check.Detail = "CA bundle missing or invalid"
		return check
	}
	intermediates := x509.NewCertPool()
	for _, der := range identity.Certificate[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			check.Detail = "invalid intermediate certificate"
			return check
		}
		intermediates.AddCert(cert)
	}
	purpose := x509.ExtKeyUsageServerAuth
	if options.purpose == "client" {
		purpose = x509.ExtKeyUsageClientAuth
	} else if options.purpose != "server" || options.hostname == "" {
		check.Detail = "use purpose server with actual DNS name, or purpose client"
		return check
	}
	if options.minDays < 0 || options.minDays > 3650 {
		check.Detail = "validity horizon must be between 0 and 3650 days"
		return check
	}
	now := time.Now()
	for _, at := range []time.Time{now, now.Add(time.Duration(options.minDays) * 24 * time.Hour)} {
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: options.hostname, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{purpose}})
		if err != nil {
			check.Detail = "chain/purpose/hostname invalid, or certificate chain expires within the validity horizon"
			return check
		}
	}
	check.Passed, check.Detail = true, fmt.Sprintf("key pair, chain, purpose and DNS verified now and %d days ahead; expiry=%s", options.minDays, leaf.NotAfter.UTC().Format(time.RFC3339))
	return check
}

type rolloutSecurityContext struct {
	RunAsUser *int64 `json:"runAsUser"`
	FSGroup   *int64 `json:"fsGroup"`
}
type rolloutContainer struct {
	SecurityContext rolloutSecurityContext `json:"securityContext"`
	VolumeMounts    []struct {
		Name string `json:"name"`
	} `json:"volumeMounts"`
}
type rolloutPod struct {
	SecurityContext rolloutSecurityContext `json:"securityContext"`
	Containers      []rolloutContainer     `json:"containers"`
	InitContainers  []rolloutContainer     `json:"initContainers"`
	Volumes         []struct {
		Name      string          `json:"name"`
		Projected json.RawMessage `json:"projected"`
		Secret    *struct {
			DefaultMode *int `json:"defaultMode"`
			Items       []struct {
				Mode *int `json:"mode"`
			} `json:"items"`
		} `json:"secret"`
	} `json:"volumes"`
}

func includeStatefulSetClaimVolumes(value any) {
	switch v := value.(type) {
	case map[string]any:
		if v["kind"] == "StatefulSet" {
			spec, _ := v["spec"].(map[string]any)
			template, _ := spec["template"].(map[string]any)
			podSpec, _ := template["spec"].(map[string]any)
			volumes, _ := podSpec["volumes"].([]any)
			known := map[string]bool{}
			for _, raw := range volumes {
				volume, _ := raw.(map[string]any)
				if name, _ := volume["name"].(string); name != "" {
					known[name] = true
				}
			}
			claims, _ := spec["volumeClaimTemplates"].([]any)
			for _, raw := range claims {
				claim, _ := raw.(map[string]any)
				metadata, _ := claim["metadata"].(map[string]any)
				name, _ := metadata["name"].(string)
				if name != "" && !known[name] {
					volumes = append(volumes, map[string]any{"name": name})
					known[name] = true
				}
			}
			if podSpec != nil {
				podSpec["volumes"] = volumes
			}
		}
		for _, child := range v {
			includeStatefulSetClaimVolumes(child)
		}
	case []any:
		for _, child := range v {
			includeStatefulSetClaimVolumes(child)
		}
	}
}

func checkRolloutMounts(path string) deploymentCredentialCheck {
	check := deploymentCredentialCheck{Name: "rollout Secret mounts " + filepath.Base(path)}
	raw, err := os.ReadFile(path)
	var document any
	if err != nil || json.Unmarshal(raw, &document) != nil {
		check.Detail = "provide readable complete rendered workload JSON"
		return check
	}
	includeStatefulSetClaimVolumes(document)
	pods, secretMounts := 0, 0
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			if _, exists := v["containers"]; exists {
				encoded, _ := json.Marshal(v)
				var pod rolloutPod
				if json.Unmarshal(encoded, &pod) != nil {
					return errors.New("invalid Pod specification")
				}
				pods++
				for _, container := range append(pod.Containers, pod.InitContainers...) {
					for _, mount := range container.VolumeMounts {
						found := false
						for _, volume := range pod.Volumes {
							if volume.Name == mount.Name {
								found = true
							}
						}
						if !found {
							return errors.New("mounted volume is absent; supply complete rendered workload JSON, not a partial patch")
						}
					}
				}
				for _, volume := range pod.Volumes {
					if len(volume.Projected) > 0 && string(volume.Projected) != "null" {
						return errors.New("projected volumes require separate effective-access qualification; this check supports direct Secret volumes")
					}

					if volume.Secret == nil {
						continue
					}
					mode := 0o644
					if volume.Secret.DefaultMode != nil {
						mode = *volume.Secret.DefaultMode
					}
					modes := []int{mode}
					if len(volume.Secret.Items) > 0 {
						modes = nil
						for _, item := range volume.Secret.Items {
							m := mode
							if item.Mode != nil {
								m = *item.Mode
							}
							modes = append(modes, m)
						}
					}
					for _, container := range append(pod.Containers, pod.InitContainers...) {
						for _, mount := range container.VolumeMounts {
							if mount.Name != volume.Name {
								continue
							}
							secretMounts++
							uid := container.SecurityContext.RunAsUser
							if uid == nil {
								uid = pod.SecurityContext.RunAsUser
							}
							for _, m := range modes {
								if m < 0 || m > 0o777 {
									return errors.New("invalid Secret file mode")
								}
								if m&0o004 != 0 || (uid != nil && *uid == 0 && m&0o400 != 0) || (pod.SecurityContext.FSGroup != nil && *pod.SecurityContext.FSGroup >= 0 && m&0o040 != 0) {
									continue
								}
								return errors.New("restricted Secret mount lacks provable read access; declare matching Pod fsGroup and group-read mode")
							}
						}
					}
				}
			}
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(document); err != nil {
		check.Detail = err.Error()
		return check
	}
	if pods == 0 || secretMounts == 0 {
		check.Detail = "no mounted direct Secret volume found; provide the complete affected workload JSON"
		return check
	}
	check.Passed, check.Detail = true, "restricted Secret mount declarations checked; live mounted-file access still requires qualification"
	return check
}

func (o *deploymentCredentialCheckOptions) configureChecks(selection string) error {
	if selection != "" {
		o.selected = map[string]bool{}
		for _, name := range strings.Split(selection, ",") {
			name = strings.TrimSpace(name)
			if !keySet("linode", "ghcr", "dns", "storage", "tls", "mounts")[name] {
				return errors.New("unknown --checks entry; choose linode,ghcr,dns,storage,tls,mounts")
			}
			o.selected[name] = true
		}
	}
	if len(o.images) > 0 && o.selected != nil && !o.selected["ghcr"] {
		return errors.New("--image requires ghcr in --checks")
	}
	for _, image := range o.images {
		if !rolloutImagePattern.MatchString(image) {
			return errors.New("--image requires a reviewed GHCR @sha256 reference")
		}
	}
	tlsRequested := o.tls.cert != "" || o.tls.key != "" || o.tls.ca != "" || o.tls.hostname != "" || o.selected["tls"]
	if tlsRequested {
		if o.tls.cert == "" || o.tls.key == "" || o.tls.ca == "" {
			return errors.New("TLS checks require --tls-cert, --tls-key and --tls-ca")
		}
		if o.tls.purpose != "client" && o.tls.purpose != "server" {
			return errors.New("--tls-purpose must be server or client")
		}
		if o.tls.purpose == "server" && o.tls.hostname == "" {
			return errors.New("server TLS requires --tls-name")
		}
		if o.tls.minDays < 0 || o.tls.minDays > 3650 {
			return errors.New("--min-valid-days must be between 0 and 3650")
		}
		if o.selected != nil && !o.selected["tls"] {
			return errors.New("TLS inputs require tls in --checks")
		}
	}
	if len(o.manifests) > 0 && o.selected != nil && !o.selected["mounts"] {
		return errors.New("--manifest requires mounts in --checks")
	}
	if o.selected["mounts"] && len(o.manifests) == 0 {
		return errors.New("mounts requires --manifest")
	}
	return nil
}
