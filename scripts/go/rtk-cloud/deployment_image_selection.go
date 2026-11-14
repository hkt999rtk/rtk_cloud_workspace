package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The environment selection format is shared with the version updater. It is
// configuration: the selected immutable reference takes precedence for each
// explicitly named role, without changing any other role or setting.
const environmentImagesKey = "DEPLOYMENT_IMAGES"

type environmentImageSelection struct {
	Environment string                             `json:"environment"`
	Images      map[string]environmentReleaseImage `json:"images"`
}

func environmentVersionRoles() map[string]string {
	roles := map[string]string{
		"LKE_MQTT_PKI_IMAGE":               "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki",
		"LKE_OPENBAO_PKI_IMAGE":            "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki",
		"LKE_PKI_CONTROLLER_IMAGE":         "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api",
		"LKE_PKI_MANAGEMENT_IMAGE":         "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api",
		"LKE_CERTISSUER_IMAGE":             "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api",
		"LKE_POSTGRES_BACKUP_RUNNER_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_workspace/postgres-backup",
	}
	for _, source := range lkeServiceImageSources() {
		roles[source.EnvKey] = "ghcr.io/hkt999rtk/" + source.RepoName + "/" + source.Name
	}
	return roles
}

func parseEnvironmentImageSelection(environment, raw string) (environmentImageSelection, error) {
	selection := environmentImageSelection{Environment: environment}
	if raw == "" {
		return selection, nil
	}
	if len(raw) > int(environmentInputMaxBytes) || decodeEnvironmentJSON([]byte(raw), &selection) != nil || selection.Environment != environment || len(selection.Images) == 0 {
		return selection, errors.New("DEPLOYMENT_IMAGES must contain images for the selected environment")
	}
	roles := environmentVersionRoles()
	for key, image := range selection.Images {
		if roles[key] == "" || !immutableEnvironmentImage.MatchString(image.Reference) || !strings.HasPrefix(image.Reference, roles[key]+"@") {
			return selection, errors.New("DEPLOYMENT_IMAGES contains an unknown role or invalid immutable image")
		}
	}
	return selection, nil
}

func projectEnvironmentImageSelection(environment string, values map[string]string) error {
	selection, err := parseEnvironmentImageSelection(environment, values[environmentImagesKey])
	if err != nil {
		return err
	}
	for key, image := range selection.Images {
		values[strings.TrimPrefix(key, "LKE_")] = image.Reference
	}
	return nil
}

func readEnvironmentImageSelection(store secretStore) (environmentImageSelection, error) {
	path := filepath.Join(store.Root, "operator", "env", environmentImagesKey)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return environmentImageSelection{Environment: store.Environment}, nil
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return environmentImageSelection{}, err
	}
	return parseEnvironmentImageSelection(store.Environment, string(raw))
}
