package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/internal/storagepolicy"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

type linodeDestroyPlan struct {
	Stack               string
	ConfirmText         string
	LKEClusters         []linodeDestroyResource
	Instances           []linodeDestroyResource
	Firewalls           []linodeDestroyResource
	VPCs                []linodeDestroyResource
	ObjectBuckets       []linodeDestroyResource
	OrphanVolumes       []linodeDestroyResource
	OrphanNodeBalancers []linodeDestroyResource
}

type linodeDestroyResource struct {
	ID     string
	Label  string
	Region string
	Status string
	Path   string
}

func runDestroyLinodeStagingResources(args []string) error {
	if !hasFlag(args, "--loadtest-prefix") {
		args = append(args, "--loadtest-prefix", "home-100k")
	}
	return runDestroyEnvironmentResources(args)
}

func runDestroyEnvironmentResources(args []string) error {
	fs := flag.NewFlagSet("destroy-environment-resources", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	loadTestPrefix := fs.String("loadtest-prefix", "", "optional load generator VM label/tag prefix")
	yes := fs.Bool("yes", false, "delete resources after listing the plan")
	confirmText := fs.String("confirm-text", "", "confirmation text; must equal: destroy <stack>")
	includeObjectStorage := fs.Bool("include-object-storage", false, "delete exact configured runtime/OTA Object Storage buckets too; buckets must already be empty")
	includeOrphanVolumes := fs.Bool("include-orphan-volumes", false, "delete unattached orphan pvc-* Block Storage volumes listed by --orphan-volume-ids")
	orphanVolumeIDs := fs.String("orphan-volume-ids", "", "comma-separated Linode volume IDs allowed for orphan pvc-* deletion")
	onlyOrphanVolumes := fs.Bool("only-orphan-volumes", false, "skip staging runtime resources and delete only exact unattached pvc-* volumes allowed by --orphan-volume-ids")
	includeOrphanNodeBalancers := fs.Bool("include-orphan-nodebalancers", false, "delete orphan LKE NodeBalancers listed by --orphan-nodebalancer-ids")
	orphanNodeBalancerIDs := fs.String("orphan-nodebalancer-ids", "", "comma-separated Linode NodeBalancer IDs allowed for orphan deletion")
	onlyOrphanNodeBalancers := fs.Bool("only-orphan-nodebalancers", false, "skip staging runtime resources and delete only exact orphan LKE NodeBalancers allowed by --orphan-nodebalancer-ids")
	onlyLKECluster := fs.Bool("only-lke-cluster", false, "skip non-LKE Linode resources and delete only matched LKE clusters plus local kubeconfig/LKE state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *onlyLKECluster && (*onlyOrphanVolumes || *onlyOrphanNodeBalancers) {
		return errors.New("--only-lke-cluster cannot be combined with orphan-only cleanup modes")
	}
	if *onlyOrphanVolumes && *onlyOrphanNodeBalancers {
		return errors.New("--only-orphan-volumes cannot be combined with --only-orphan-nodebalancers")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	token := resolveLinodeToken(envRoot)
	if token == "" {
		return errors.New("LINODE_TOKEN is required in ~/.config/rtk_cloud/<environment>/operator/env/LINODE_TOKEN")
	}

	stackValues, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if stackValues == nil {
		stackValues = map[string]string{}
	}
	envValues := envroot.Derive(stackValues)
	stack := firstNonEmpty(envValues["CLOUD_STACK_NAME"], "video-cloud-staging")
	_, _, providerState, err := readLKEStateForTarget(provisionPaths{EnvRoot: envRoot}, stackValues)
	if err != nil {
		return err
	}
	if id := providerState["LKE_CLUSTER_ID"]; id != "" {
		envValues["LKE_CLUSTER_ID"] = id
	}
	if *includeObjectStorage && !*onlyLKECluster {
		if err := validateDestroyBackupConfigurations(workspace, envRoot, envValues); err != nil {
			return err
		}
	}
	plan, err := buildLinodeDestroyPlan(token, envValues, stack, *loadTestPrefix)
	if err != nil {
		return err
	}
	orphanVolumeIDSet, err := parseDestroyIDSet(*orphanVolumeIDs)
	if err != nil {
		return err
	}
	orphanNodeBalancerIDSet, err := parseDestroyIDSet(*orphanNodeBalancerIDs)
	if err != nil {
		return err
	}
	printLinodeDestroyPlan(os.Stdout, plan, *includeObjectStorage, *includeOrphanVolumes, orphanVolumeIDSet, *includeOrphanNodeBalancers, orphanNodeBalancerIDSet, *onlyOrphanVolumes, *onlyOrphanNodeBalancers, *onlyLKECluster)
	if !*yes {
		fmt.Fprintf(os.Stdout, "dry-run only; pass --yes --confirm-text %q to delete the listed non-skipped resources\n", plan.ConfirmText)
		return nil
	}
	if strings.TrimSpace(*confirmText) == "" {
		fmt.Fprintf(os.Stderr, `Type %q to continue: `, plan.ConfirmText)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		*confirmText = strings.TrimSpace(answer)
	}
	if *confirmText != plan.ConfirmText {
		return fmt.Errorf("confirmation text must be %q", plan.ConfirmText)
	}
	return executeLinodeDestroyPlan(token, envRoot, plan, *includeObjectStorage, *includeOrphanVolumes, orphanVolumeIDSet, *includeOrphanNodeBalancers, orphanNodeBalancerIDSet, *onlyOrphanVolumes, *onlyOrphanNodeBalancers, *onlyLKECluster)
}

func buildLinodeDestroyPlan(token string, env map[string]string, stack, loadTestPrefix string) (linodeDestroyPlan, error) {
	match := linodeDestroyMatcher(env, stack, loadTestPrefix)
	plan := linodeDestroyPlan{
		Stack:       stack,
		ConfirmText: "destroy " + stack,
	}
	if clusters, err := listDestroyLKEClusters(token, match); err != nil {
		return plan, err
	} else {
		plan.LKEClusters = clusters
	}
	if instances, err := listDestroyInstances(token, match); err != nil {
		return plan, err
	} else {
		plan.Instances = instances
	}
	if firewalls, err := listDestroyFirewalls(token, match); err != nil {
		return plan, err
	} else {
		plan.Firewalls = firewalls
	}
	if vpcs, err := listDestroyVPCs(token, match); err != nil {
		return plan, err
	} else {
		plan.VPCs = vpcs
	}
	if buckets, err := listDestroyObjectBuckets(token, env); err != nil {
		return plan, err
	} else {
		plan.ObjectBuckets = buckets
	}
	if volumes, err := listDestroyOrphanVolumes(token); err != nil {
		return plan, err
	} else {
		plan.OrphanVolumes = volumes
	}
	if nodeBalancers, err := listDestroyOrphanNodeBalancers(token, env); err != nil {
		return plan, err
	} else {
		plan.OrphanNodeBalancers = nodeBalancers
	}
	return plan, nil
}

func linodeDestroyMatcher(env map[string]string, stack, loadTestPrefix string) func(string, []string) bool {
	prefixes := []string{
		stack,
		lkeName(stack),
		env["VIDEO_CLOUD_LABEL_PREFIX"],
		env["ACCOUNT_MANAGER_LINODE_LABEL"],
		env["ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL"],
		env["ADMIN_LINODE_LABEL"],
		env["ADMIN_LINODE_FIREWALL_LABEL"],
		env["CLOUD_LOGGER_LINODE_LABEL"],
		env["CLOUD_LOGGER_LINODE_FIREWALL_LABEL"],
		env["VIDEO_CLOUD_VPC_LABEL"],
	}
	if loadTestPrefix != "" {
		prefixes = append(prefixes, loadTestPrefix)
	}
	seen := map[string]bool{}
	filtered := []string{}
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" || seen[prefix] {
			continue
		}
		seen[prefix] = true
		filtered = append(filtered, prefix)
	}
	return func(label string, tags []string) bool {
		for _, prefix := range filtered {
			if label == prefix || strings.HasPrefix(label, prefix+"-") {
				return true
			}
			for _, tag := range tags {
				if tag == prefix {
					return true
				}
			}
		}
		return false
	}
}

func listDestroyLKEClusters(token string, match func(string, []string) bool) ([]linodeDestroyResource, error) {
	var listed struct {
		Data []struct {
			ID     int      `json:"id"`
			Label  string   `json:"label"`
			Region string   `json:"region"`
			Tags   []string `json:"tags"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/lke/clusters?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		if match(item.Label, item.Tags) {
			out = append(out, linodeDestroyResource{ID: fmt.Sprint(item.ID), Label: item.Label, Region: item.Region, Path: fmt.Sprintf("/lke/clusters/%d", item.ID)})
		}
	}
	return sortedDestroyResources(out), nil
}

func listDestroyInstances(token string, match func(string, []string) bool) ([]linodeDestroyResource, error) {
	var listed struct {
		Data []struct {
			ID     int      `json:"id"`
			Label  string   `json:"label"`
			Region string   `json:"region"`
			Status string   `json:"status"`
			Tags   []string `json:"tags"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/linode/instances?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		if match(item.Label, item.Tags) {
			out = append(out, linodeDestroyResource{ID: fmt.Sprint(item.ID), Label: item.Label, Region: item.Region, Status: item.Status, Path: fmt.Sprintf("/linode/instances/%d", item.ID)})
		}
	}
	return sortedDestroyResources(out), nil
}

func listDestroyFirewalls(token string, match func(string, []string) bool) ([]linodeDestroyResource, error) {
	var listed struct {
		Data []struct {
			ID    int      `json:"id"`
			Label string   `json:"label"`
			Tags  []string `json:"tags"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/networking/firewalls?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		if match(item.Label, item.Tags) {
			out = append(out, linodeDestroyResource{ID: fmt.Sprint(item.ID), Label: item.Label, Path: fmt.Sprintf("/networking/firewalls/%d", item.ID)})
		}
	}
	return sortedDestroyResources(out), nil
}

func listDestroyVPCs(token string, match func(string, []string) bool) ([]linodeDestroyResource, error) {
	var listed struct {
		Data []struct {
			ID     int      `json:"id"`
			Label  string   `json:"label"`
			Region string   `json:"region"`
			Tags   []string `json:"tags"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/vpcs?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		if match(item.Label, item.Tags) {
			out = append(out, linodeDestroyResource{ID: fmt.Sprint(item.ID), Label: item.Label, Region: item.Region, Path: fmt.Sprintf("/vpcs/%d", item.ID)})
		}
	}
	return sortedDestroyResources(out), nil
}

func listDestroyObjectBuckets(token string, env map[string]string) ([]linodeDestroyResource, error) {
	// Bucket ownership comes only from the materialized runtime bindings. Stack
	// prefixes and tags also match shared or retained data and cannot authorize
	// bucket deletion. Artifact, backup and report bindings are never included.
	type identity struct{ bucket, region string }
	owned := map[identity]bool{}
	for _, prefix := range []string{"VIDEO_CLOUD_BLOB_", "VIDEO_CLOUD_OTA_BLOB_"} {
		bucket, region := strings.TrimSpace(env[prefix+"BUCKET"]), strings.TrimSpace(env[prefix+"REGION"])
		if bucket != "" && region != "" {
			if err := validateDestroyRuntimeBucket(env, bucket, region); err != nil {
				return nil, err
			}
			owned[identity{bucket, region}] = true
		}
	}
	if len(owned) == 0 {
		return nil, nil
	}
	var listed struct {
		Data []struct {
			Label   string `json:"label"`
			Region  string `json:"region"`
			Cluster string `json:"cluster"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/object-storage/buckets?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		region := firstNonEmpty(item.Region, item.Cluster)
		if !owned[identity{item.Label, region}] {
			continue
		}
		path := fmt.Sprintf("/object-storage/buckets/%s/%s", region, item.Label)
		out = append(out, linodeDestroyResource{ID: item.Label, Label: item.Label, Region: region, Path: path})
	}
	return sortedDestroyResources(out), nil
}

func validateDestroyRuntimeBucket(env map[string]string, bucket, region string) error {
	// Retain the registered legacy shared artifact identity during migration.
	// Canonical identities are generated by the shared policy, never prefix-matched.
	if bucket == "rtk-cloud-client-artifacts" && region == "us-sea" {
		return fmt.Errorf("runtime bucket %s in %s is registered shared artifact storage and cannot be destroyed", bucket, region)
	}
	shared, err := storagepolicy.Bucket("shared", "artifacts", region)
	if err == nil && bucket == shared {
		return fmt.Errorf("runtime bucket %s is shared artifact storage and cannot be destroyed", bucket)
	}
	if err := validateDestroyRegisteredStorage(env, bucket, region); err != nil {
		return err
	}
	for _, prefix := range []string{
		"SDK_ARTIFACT_", "RELEASE_ARTIFACT_STORAGE_", "REPORT_OBJECT_STORAGE_",
		"RTK_BACKUP_", "BACKUP_OBJECT_STORAGE_", "PKI_BACKUP_", "PKI_OBJECT_STORAGE_",
	} {
		retained := strings.TrimSpace(env[prefix+"BUCKET"])
		retainedRegion := strings.TrimSpace(env[prefix+"REGION"])
		if retained == bucket && (retainedRegion == "" || retainedRegion == region) {
			return fmt.Errorf("runtime bucket %s overlaps retained storage binding %sBUCKET; refusing bucket destruction", bucket, prefix)
		}
	}
	return nil
}

func listDestroyOrphanVolumes(token string) ([]linodeDestroyResource, error) {
	all, err := listLinodeVolumes(token)
	if err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range all {
		if item.LinodeID != nil || item.Status != "active" || !strings.HasPrefix(item.Label, "pvc-") {
			continue
		}
		out = append(out, linodeDestroyResource{
			ID:     item.ID,
			Label:  item.Label,
			Region: item.Region,
			Status: "unattached",
			Path:   "/volumes/" + item.ID,
		})
	}
	return sortedDestroyResources(out), nil
}

type linodeVolume struct {
	ID       string
	Label    string
	Region   string
	Status   string
	LinodeID *int
}

func listLinodeVolumes(token string) ([]linodeVolume, error) {
	var listed struct {
		Data []struct {
			ID       int      `json:"id"`
			Label    string   `json:"label"`
			Region   string   `json:"region"`
			Status   string   `json:"status"`
			LinodeID *int     `json:"linode_id"`
			Tags     []string `json:"tags"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/volumes?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeVolume{}
	for _, item := range listed.Data {
		out = append(out, linodeVolume{
			ID:       strconv.Itoa(item.ID),
			Label:    item.Label,
			Region:   item.Region,
			Status:   item.Status,
			LinodeID: item.LinodeID,
		})
	}
	return out, nil
}

func deleteDeploymentVolumesWhenDetached(token string, volumeIDs []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), max(timeout, time.Second))
	defer cancel()
	return deleteDeploymentVolumesWithEvidence(ctx, token, volumeIDs, timeout, readRemovalRecoveryNative, func(token, id string) error {
		_, err := linodeRequestRaw(token, "DELETE", "/volumes/"+id, "")
		if err == nil {
			fmt.Fprintf(os.Stdout, "DELETE REQUEST ACCEPTED: original Linode volume id=%s\n", id)
		}
		return err
	})
}

func deleteDeploymentVolumesWithEvidence(ctx context.Context, token string, volumeIDs []string, timeout time.Duration, read removalRecoveryReader, deleteVolume func(string, string) error) error {
	if len(volumeIDs) == 0 {
		return nil
	}
	if strings.TrimSpace(token) == "" || read == nil || deleteVolume == nil {
		return errors.New("selected credentials and exact volume evidence readers are required")
	}
	wanted := map[string]bool{}
	for _, id := range volumeIDs {
		if _, err := positiveIntValue("volume_id", id); err != nil || wanted[id] {
			return errors.New("original volume IDs must be distinct positive numeric IDs")
		}
		wanted[id] = true
	}
	ids := append([]string(nil), volumeIDs...)
	sort.Strings(ids)
	deadline := time.Now().Add(timeout)
	for {
		pending := []string{}
		for _, id := range ids {
			raw, err := read(ctx, token, "/volumes/"+id, nil)
			if errors.Is(err, errRemovalRecoveryNativeNotFound) {
				continue
			}
			var volume removalRecoveryVolume
			if err != nil || json.Unmarshal(raw, &volume) != nil || strconv.Itoa(volume.ID) != id {
				return fmt.Errorf("cannot verify original Linode volume %s through its exact native endpoint", id)
			}
			if volume.LinodeID != nil || volume.Status != "active" {
				pending = append(pending, id)
				continue
			}
			deleteErr := deleteVolume(token, id)
			raw, err = read(ctx, token, "/volumes/"+id, nil)
			if errors.Is(err, errRemovalRecoveryNativeNotFound) {
				fmt.Fprintf(os.Stdout, "[VERIFIED] original Linode volume id=%s is absent\n", id)
				continue
			}
			if err != nil || json.Unmarshal(raw, &volume) != nil || strconv.Itoa(volume.ID) != id {
				return fmt.Errorf("original Linode volume %s deletion cannot be verified; permission and transport failures are not absence", id)
			}
			if deleteErr != nil {
				return fmt.Errorf("original Linode volume %s deletion failed and its exact native endpoint still reports it present", id)
			}
			pending = append(pending, id)
		}
		if len(pending) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for exact original Linode volumes to be absent: %s", timeout, strings.Join(pending, ", "))
		}
		timer := time.NewTimer(min(5*time.Second, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("exact original volume removal verification timed out")
		case <-timer.C:
		}
	}
}

func listDestroyOrphanNodeBalancers(token string, env map[string]string) ([]linodeDestroyResource, error) {
	currentClusterID := strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_CLUSTER_ID"), env["LKE_CLUSTER_ID"]))
	var listed struct {
		Data []struct {
			ID     int      `json:"id"`
			Label  string   `json:"label"`
			Region string   `json:"region"`
			Tags   []string `json:"tags"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/nodebalancers?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		if !strings.HasPrefix(item.Label, "lke") {
			continue
		}
		if currentClusterID != "" && strings.HasPrefix(item.Label, "lke"+currentClusterID+"-") {
			continue
		}
		out = append(out, linodeDestroyResource{
			ID:     fmt.Sprint(item.ID),
			Label:  item.Label,
			Region: item.Region,
			Status: "orphan-candidate",
			Path:   fmt.Sprintf("/nodebalancers/%d", item.ID),
		})
	}
	return sortedDestroyResources(out), nil
}

func linodeDestroyList(token, path string, target any) error {
	out, err := linodeRequestRaw(token, "GET", path, "")
	if err != nil {
		return err
	}
	return json.Unmarshal(out, target)
}

func sortedDestroyResources(resources []linodeDestroyResource) []linodeDestroyResource {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Label == resources[j].Label {
			return resources[i].ID < resources[j].ID
		}
		return resources[i].Label < resources[j].Label
	})
	return resources
}

func parseDestroyIDSet(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		for _, ch := range id {
			if ch < '0' || ch > '9' {
				return nil, fmt.Errorf("--orphan-volume-ids must contain numeric ids only, got %q", id)
			}
		}
		out[id] = true
	}
	return out, nil
}

func printLinodeDestroyPlan(w *os.File, plan linodeDestroyPlan, includeObjectStorage bool, includeOrphanVolumes bool, orphanVolumeIDs map[string]bool, includeOrphanNodeBalancers bool, orphanNodeBalancerIDs map[string]bool, onlyOrphanVolumes bool, onlyOrphanNodeBalancers bool, onlyLKECluster bool) {
	fmt.Fprintf(w, "Linode destructive cleanup plan for stack %q\n", plan.Stack)
	printDestroyGroup(w, "LKE clusters", plan.LKEClusters, onlyOrphanVolumes || onlyOrphanNodeBalancers)
	printDestroyGroup(w, "Linode instances", plan.Instances, onlyOrphanVolumes || onlyOrphanNodeBalancers || onlyLKECluster)
	printDestroyGroup(w, "Firewalls", plan.Firewalls, onlyOrphanVolumes || onlyOrphanNodeBalancers || onlyLKECluster)
	printDestroyGroup(w, "VPCs", plan.VPCs, onlyOrphanVolumes || onlyOrphanNodeBalancers || onlyLKECluster)
	printDestroyGroup(w, "Object Storage buckets", plan.ObjectBuckets, onlyOrphanVolumes || onlyOrphanNodeBalancers || onlyLKECluster || !includeObjectStorage)
	printDestroyVolumeGroup(w, "Unattached pvc-* Block Storage volumes", plan.OrphanVolumes, includeOrphanVolumes && !onlyLKECluster, orphanVolumeIDs)
	printDestroyExactIDGroup(w, "Orphan LKE NodeBalancers", plan.OrphanNodeBalancers, includeOrphanNodeBalancers && !onlyLKECluster, orphanNodeBalancerIDs)
	if onlyOrphanVolumes {
		fmt.Fprintln(w, "Only orphan volume mode is enabled; staging runtime resources are listed but skipped.")
	}
	if onlyOrphanNodeBalancers {
		fmt.Fprintln(w, "Only orphan NodeBalancer mode is enabled; staging runtime resources are listed but skipped.")
	}
	if onlyLKECluster {
		fmt.Fprintln(w, "Only LKE cluster mode is enabled; non-LKE Linode resources are listed but skipped.")
	}
	if len(plan.ObjectBuckets) > 0 && !includeObjectStorage {
		fmt.Fprintln(w, "Object Storage buckets are listed only; pass --include-object-storage to delete matched empty buckets.")
	}
	if len(plan.OrphanVolumes) > 0 {
		fmt.Fprintln(w, "Unattached pvc-* Block Storage volumes are listed only; pass --include-orphan-volumes with --orphan-volume-ids <id,id> to delete exact volume IDs.")
	}
	if len(plan.OrphanNodeBalancers) > 0 {
		fmt.Fprintln(w, "Orphan LKE NodeBalancers are listed only; pass --include-orphan-nodebalancers with --orphan-nodebalancer-ids <id,id> to delete exact NodeBalancer IDs.")
	}
}

func printDestroyGroup(w *os.File, title string, resources []linodeDestroyResource, skipped bool) {
	fmt.Fprintf(w, "\n%s (%d)\n", title, len(resources))
	if len(resources) == 0 {
		fmt.Fprintln(w, "- none")
		return
	}
	for _, item := range resources {
		status := item.Status
		if status == "" {
			status = "-"
		}
		region := item.Region
		if region == "" {
			region = "-"
		}
		if skipped {
			fmt.Fprintf(w, "- SKIP id=%s label=%s region=%s status=%s\n", item.ID, item.Label, region, status)
		} else {
			fmt.Fprintf(w, "- DELETE id=%s label=%s region=%s status=%s\n", item.ID, item.Label, region, status)
		}
	}
}

func printDestroyVolumeGroup(w *os.File, title string, resources []linodeDestroyResource, includeOrphanVolumes bool, orphanVolumeIDs map[string]bool) {
	printDestroyExactIDGroup(w, title, resources, includeOrphanVolumes, orphanVolumeIDs)
}

func printDestroyExactIDGroup(w *os.File, title string, resources []linodeDestroyResource, include bool, allowedIDs map[string]bool) {
	fmt.Fprintf(w, "\n%s (%d)\n", title, len(resources))
	if len(resources) == 0 {
		fmt.Fprintln(w, "- none")
		return
	}
	for _, item := range resources {
		status := item.Status
		if status == "" {
			status = "-"
		}
		region := item.Region
		if region == "" {
			region = "-"
		}
		if include && allowedIDs[item.ID] {
			fmt.Fprintf(w, "- DELETE id=%s label=%s region=%s status=%s\n", item.ID, item.Label, region, status)
		} else {
			fmt.Fprintf(w, "- SKIP id=%s label=%s region=%s status=%s\n", item.ID, item.Label, region, status)
		}
	}
}

func executeLinodeDestroyPlan(token, envRoot string, plan linodeDestroyPlan, includeObjectStorage bool, includeOrphanVolumes bool, orphanVolumeIDs map[string]bool, includeOrphanNodeBalancers bool, orphanNodeBalancerIDs map[string]bool, onlyOrphanVolumes bool, onlyOrphanNodeBalancers bool, onlyLKECluster bool) error {
	var localState []destroyedLKEFile
	var store secretStore
	if !onlyOrphanVolumes && !onlyOrphanNodeBalancers && len(plan.LKEClusters) > 0 {
		var err error
		store, localState, err = snapshotDestroyedLKEState(envRoot, plan)
		if err != nil {
			return err
		}
	}
	if !onlyOrphanVolumes && !onlyOrphanNodeBalancers {
		groups := [][]linodeDestroyResource{plan.LKEClusters}
		if !onlyLKECluster {
			groups = append(groups, plan.Instances, plan.Firewalls, plan.VPCs)
		}
		for _, group := range groups {
			for _, item := range group {
				if item.Path == "" {
					continue
				}
				fmt.Fprintf(os.Stdout, "deleting %s (%s)\n", item.Label, item.Path)
				if _, err := linodeRequestRaw(token, "DELETE", item.Path, ""); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "DELETE REQUEST ACCEPTED: %s (%s)\n", item.Label, item.Path)
			}
		}
		if len(localState) > 0 {
			if err := removeDestroyedLKEState(store, localState); err != nil {
				return err
			}
		}
	}
	if includeObjectStorage && !onlyLKECluster {
		for _, item := range plan.ObjectBuckets {
			if item.Path == "" {
				return fmt.Errorf("cannot delete Object Storage bucket %q without a region", item.Label)
			}
			fmt.Fprintf(os.Stdout, "deleting %s (%s)\n", item.Label, item.Path)
			if _, err := linodeRequestRaw(token, "DELETE", item.Path, ""); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "DELETE REQUEST ACCEPTED: %s (%s)\n", item.Label, item.Path)
		}
	}
	if includeOrphanVolumes && !onlyLKECluster {
		if len(orphanVolumeIDs) == 0 {
			return errors.New("--include-orphan-volumes requires --orphan-volume-ids with exact Linode volume IDs")
		}
		for _, item := range plan.OrphanVolumes {
			if !orphanVolumeIDs[item.ID] {
				continue
			}
			if item.Path == "" {
				return fmt.Errorf("cannot delete orphan volume %q without an API path", item.Label)
			}
			fmt.Fprintf(os.Stdout, "deleting %s (%s)\n", item.Label, item.Path)
			if _, err := linodeRequestRaw(token, "DELETE", item.Path, ""); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "DELETE REQUEST ACCEPTED: %s (%s)\n", item.Label, item.Path)
		}
	}
	if includeOrphanNodeBalancers && !onlyLKECluster {
		if len(orphanNodeBalancerIDs) == 0 {
			return errors.New("--include-orphan-nodebalancers requires --orphan-nodebalancer-ids with exact Linode NodeBalancer IDs")
		}
		for _, item := range plan.OrphanNodeBalancers {
			if !orphanNodeBalancerIDs[item.ID] {
				continue
			}
			if item.Path == "" {
				return fmt.Errorf("cannot delete orphan NodeBalancer %q without an API path", item.Label)
			}
			fmt.Fprintf(os.Stdout, "deleting %s (%s)\n", item.Label, item.Path)
			if _, err := linodeRequestRaw(token, "DELETE", item.Path, ""); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "DELETE REQUEST ACCEPTED: %s (%s)\n", item.Label, item.Path)
		}
	}
	return nil
}

type destroyedLKEFile struct {
	Relative string
	Path     string
	Raw      []byte
	Info     os.FileInfo
}

func snapshotDestroyedLKEState(envRoot string, plan linodeDestroyPlan) (secretStore, []destroyedLKEFile, error) {
	environment, stack, err := lkeProviderTarget(provisionPaths{EnvRoot: envRoot}, nil)
	if err != nil || stack != plan.Stack {
		return secretStore{}, nil, errors.New("selected private environment state does not match the destruction plan")
	}
	state, err := readLKEProviderState(environment, stack)
	if err != nil {
		return secretStore{}, nil, err
	}
	if state["LKE_CLUSTER_ID"] != "" {
		matched := false
		for _, cluster := range plan.LKEClusters {
			matched = matched || cluster.ID == state["LKE_CLUSTER_ID"]
		}
		if !matched {
			return secretStore{}, nil, errors.New("destruction plan does not include the original recorded cluster; its private state must remain")
		}
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return store, nil, err
	}
	var files []destroyedLKEFile
	for _, relative := range []string{"deployment/adapters/lke/state.env", "kube/kubeconfig.yaml"} {
		path, err := privateDeploymentStatePath(environment, relative)
		if err != nil {
			return store, nil, err
		}
		before, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return store, nil, err
		}
		raw, err := readStorageEvidenceFile(path)
		after, statErr := os.Lstat(path)
		if err != nil || statErr != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			return store, nil, errors.New("private provider state changed while preparing destruction")
		}
		files = append(files, destroyedLKEFile{relative, path, raw, after})
	}
	return store, files, nil
}

func removeDestroyedLKEState(store secretStore, files []destroyedLKEFile) error {
	archive := "migration-backup/destroy-lke-" + postgresBackupRandomID()
	// A concurrent writer must retain all of its files, even after cloud deletion.
	for _, file := range files {
		current, err := os.Lstat(file.Path)
		raw, readErr := readStorageEvidenceFile(file.Path)
		if err != nil || readErr != nil || !os.SameFile(file.Info, current) || !file.Info.ModTime().Equal(current.ModTime()) || !bytes.Equal(raw, file.Raw) {
			return errors.New("private provider state changed after the approved destruction baseline; retain it and reconcile the actual outcome")
		}
	}
	for _, file := range files {
		path, err := privateDeploymentStatePath(store.Environment, filepath.Join(archive, file.Relative))
		if err != nil || path != filepath.Join(store.Root, archive, file.Relative) {
			return errors.New("private archive location or permissions changed")
		}
		if err := store.write(filepath.Join(archive, file.Relative), file.Raw, false); err != nil {
			return err
		}
	}
	for _, file := range files {
		current, err := os.Lstat(file.Path)
		if err != nil || !os.SameFile(file.Info, current) || !file.Info.ModTime().Equal(current.ModTime()) {
			return errors.New("private provider state changed during archival; concurrent data remains")
		}
		if err := os.Remove(file.Path); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stdout, "archived removed private LKE state: environment=%s files=%d\n", store.Environment, len(files))
	return nil
}
