package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var deploymentImageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (c deploymentCredentialChecker) checkRolloutImageMetadata(values map[string]string, image string) deploymentCredentialCheck {
	check := deploymentCredentialCheck{Name: "GHCR release image " + image, EvidenceLevel: "manifest-platform"}
	match := rolloutImagePattern.FindStringSubmatch(image)
	if match == nil {
		check.Code, check.Detail = "INVALID_IMAGE", "--image requires a reviewed GHCR @sha256 reference"
		return check
	}
	username, token := values["GHCR_PULL_USERNAME"], values["GHCR_PULL_TOKEN"]
	if username == "" || token == "" || strings.ContainsAny(username+token, "\r\n") {
		check.Code, check.Detail = "INVALID_CREDENTIAL", "GHCR credentials are missing or contain CR/LF"
		return check
	}
	registryToken, err := c.exchangeGHCRToken(username, token, match[1])
	if err != nil {
		check.Detail = err.Error()
		return check
	}
	digest := strings.SplitN(image, "@", 2)[1]
	for depth := 0; depth < 3; depth++ {
		raw, err := c.readRolloutMetadata(registryToken, match[1], "manifests", digest)
		if err != nil {
			check.Detail = err.Error()
			return check
		}
		var manifest struct {
			SchemaVersion int `json:"schemaVersion"`
			Manifests     []struct {
				Digest   string `json:"digest"`
				Platform struct {
					OS           string `json:"os"`
					Architecture string `json:"architecture"`
				} `json:"platform"`
			} `json:"manifests"`
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers json.RawMessage `json:"layers"`
		}
		if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 {
			check.Code, check.Detail = "INVALID_IMAGE_METADATA", "registry returned invalid image metadata"
			return check
		}
		if len(manifest.Manifests) > 0 {
			digest = ""
			for _, item := range manifest.Manifests {
				if item.Platform.OS == "linux" && item.Platform.Architecture == "amd64" {
					digest = item.Digest
					break
				}
			}
			if digest == "" {
				check.Code, check.Detail = "IMAGE_PLATFORM_MISSING", "image index has no linux/amd64 manifest"
				return check
			}
			continue
		}
		var layers []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		}
		if json.Unmarshal(manifest.Layers, &layers) != nil || layers == nil {
			check.Code, check.Detail = "INVALID_IMAGE_METADATA", "image manifest has no valid layer descriptor array"
			return check
		}
		for _, layer := range layers {
			if !deploymentImageDigestPattern.MatchString(layer.Digest) || layer.Size < 0 {
				check.Code, check.Detail = "INVALID_IMAGE_METADATA", "image manifest contains an invalid layer descriptor"
				return check
			}
		}
		raw, err = c.readRolloutMetadata(registryToken, match[1], "blobs", manifest.Config.Digest)
		if err != nil {
			check.Detail = err.Error()
			return check
		}
		var config struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		}
		if json.Unmarshal(raw, &config) != nil {
			check.Code, check.Detail = "INVALID_IMAGE_METADATA", "registry returned invalid image config"
			return check
		}
		if config.OS != "linux" || config.Architecture != "amd64" {
			check.Code, check.Detail = "IMAGE_PLATFORM_MISSING", "image config does not declare linux/amd64"
			return check
		}
		check.Passed, check.Detail = true, "selected credential, digest-bound manifest and linux/amd64 config verified; image layers not pulled"
		return check
	}
	check.Code, check.Detail = "INVALID_IMAGE_METADATA", "image index nesting exceeds the supported metadata depth"
	return check
}

func (c deploymentCredentialChecker) readRolloutMetadata(token, repository, kind, digest string) ([]byte, error) {
	if !deploymentImageDigestPattern.MatchString(digest) {
		return nil, errors.New("image metadata omitted a valid sha256 digest")
	}
	endpoint := strings.TrimRight(c.ghcrRegistryRoot, "/") + "/v2/" + repository + "/" + kind + "/" + digest
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("cannot create image metadata request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	raw, err := c.request(req)
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(raw)
	if digest != "sha256:"+hex.EncodeToString(actual[:]) {
		return nil, fmt.Errorf("image %s metadata does not match its declared digest", kind)
	}
	return raw, nil
}
