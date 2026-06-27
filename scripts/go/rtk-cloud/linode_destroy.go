package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

type linodeDestroyPlan struct {
	Stack         string
	ConfirmText   string
	LKEClusters   []linodeDestroyResource
	Instances     []linodeDestroyResource
	Firewalls     []linodeDestroyResource
	VPCs          []linodeDestroyResource
	ObjectBuckets []linodeDestroyResource
	OrphanVolumes []linodeDestroyResource
}

type linodeDestroyResource struct {
	ID     string
	Label  string
	Region string
	Status string
	Path   string
}

func runDestroyLinodeStagingResources(args []string) error {
	fs := flag.NewFlagSet("destroy-linode-staging-resources", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	loadTestPrefix := fs.String("loadtest-prefix", "home-100k", "load generator VM label/tag prefix")
	yes := fs.Bool("yes", false, "delete resources after listing the plan")
	confirmText := fs.String("confirm-text", "", "confirmation text; must equal: destroy <stack>")
	includeObjectStorage := fs.Bool("include-object-storage", false, "delete matched Object Storage buckets too; buckets must already be empty")
	includeOrphanVolumes := fs.Bool("include-orphan-volumes", false, "delete unattached orphan pvc-* Block Storage volumes listed by --orphan-volume-ids")
	orphanVolumeIDs := fs.String("orphan-volume-ids", "", "comma-separated Linode volume IDs allowed for orphan pvc-* deletion")
	onlyOrphanVolumes := fs.Bool("only-orphan-volumes", false, "skip staging runtime resources and delete only exact unattached pvc-* volumes allowed by --orphan-volume-ids")
	if err := fs.Parse(args); err != nil {
		return err
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
		return errors.New("LINODE_TOKEN is required in environment, env-root operator.env, or ~/.env")
	}

	stackValues, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if stackValues == nil {
		stackValues = map[string]string{}
	}
	envValues := envroot.Derive(stackValues)
	stack := firstNonEmpty(envValues["CLOUD_STACK_NAME"], "video-cloud-staging")
	plan, err := buildLinodeDestroyPlan(token, envValues, stack, *loadTestPrefix)
	if err != nil {
		return err
	}
	orphanVolumeIDSet, err := parseDestroyIDSet(*orphanVolumeIDs)
	if err != nil {
		return err
	}
	printLinodeDestroyPlan(os.Stdout, plan, *includeObjectStorage, *includeOrphanVolumes, orphanVolumeIDSet, *onlyOrphanVolumes)
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
	return executeLinodeDestroyPlan(token, envRoot, plan, *includeObjectStorage, *includeOrphanVolumes, orphanVolumeIDSet, *onlyOrphanVolumes)
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
	if buckets, err := listDestroyObjectBuckets(token, match); err != nil {
		return plan, err
	} else {
		plan.ObjectBuckets = buckets
	}
	if volumes, err := listDestroyOrphanVolumes(token); err != nil {
		return plan, err
	} else {
		plan.OrphanVolumes = volumes
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

func listDestroyObjectBuckets(token string, match func(string, []string) bool) ([]linodeDestroyResource, error) {
	var listed struct {
		Data []struct {
			Label   string   `json:"label"`
			Region  string   `json:"region"`
			Cluster string   `json:"cluster"`
			Tags    []string `json:"tags"`
		} `json:"data"`
	}
	if err := linodeDestroyList(token, "/object-storage/buckets?page_size=500", &listed); err != nil {
		return nil, err
	}
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		if !match(item.Label, item.Tags) {
			continue
		}
		region := firstNonEmpty(item.Region, item.Cluster)
		path := ""
		if region != "" && item.Label != "" {
			path = fmt.Sprintf("/object-storage/buckets/%s/%s", region, item.Label)
		}
		out = append(out, linodeDestroyResource{ID: item.Label, Label: item.Label, Region: region, Path: path})
	}
	return sortedDestroyResources(out), nil
}

func listDestroyOrphanVolumes(token string) ([]linodeDestroyResource, error) {
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
	out := []linodeDestroyResource{}
	for _, item := range listed.Data {
		if item.LinodeID != nil || item.Status != "active" || !strings.HasPrefix(item.Label, "pvc-") {
			continue
		}
		out = append(out, linodeDestroyResource{
			ID:     fmt.Sprint(item.ID),
			Label:  item.Label,
			Region: item.Region,
			Status: "unattached",
			Path:   fmt.Sprintf("/volumes/%d", item.ID),
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

func printLinodeDestroyPlan(w *os.File, plan linodeDestroyPlan, includeObjectStorage bool, includeOrphanVolumes bool, orphanVolumeIDs map[string]bool, onlyOrphanVolumes bool) {
	fmt.Fprintf(w, "Linode destructive cleanup plan for stack %q\n", plan.Stack)
	printDestroyGroup(w, "LKE clusters", plan.LKEClusters, onlyOrphanVolumes)
	printDestroyGroup(w, "Linode instances", plan.Instances, onlyOrphanVolumes)
	printDestroyGroup(w, "Firewalls", plan.Firewalls, onlyOrphanVolumes)
	printDestroyGroup(w, "VPCs", plan.VPCs, onlyOrphanVolumes)
	printDestroyGroup(w, "Object Storage buckets", plan.ObjectBuckets, onlyOrphanVolumes || !includeObjectStorage)
	printDestroyVolumeGroup(w, "Unattached pvc-* Block Storage volumes", plan.OrphanVolumes, includeOrphanVolumes, orphanVolumeIDs)
	if onlyOrphanVolumes {
		fmt.Fprintln(w, "Only orphan volume mode is enabled; staging runtime resources are listed but skipped.")
	}
	if len(plan.ObjectBuckets) > 0 && !includeObjectStorage {
		fmt.Fprintln(w, "Object Storage buckets are listed only; pass --include-object-storage to delete matched empty buckets.")
	}
	if len(plan.OrphanVolumes) > 0 {
		fmt.Fprintln(w, "Unattached pvc-* Block Storage volumes are listed only; pass --include-orphan-volumes with --orphan-volume-ids <id,id> to delete exact volume IDs.")
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
		if includeOrphanVolumes && orphanVolumeIDs[item.ID] {
			fmt.Fprintf(w, "- DELETE id=%s label=%s region=%s status=%s\n", item.ID, item.Label, region, status)
		} else {
			fmt.Fprintf(w, "- SKIP id=%s label=%s region=%s status=%s\n", item.ID, item.Label, region, status)
		}
	}
}

func executeLinodeDestroyPlan(token, envRoot string, plan linodeDestroyPlan, includeObjectStorage bool, includeOrphanVolumes bool, orphanVolumeIDs map[string]bool, onlyOrphanVolumes bool) error {
	if !onlyOrphanVolumes {
		for _, group := range [][]linodeDestroyResource{
			plan.LKEClusters,
			plan.Instances,
			plan.Firewalls,
			plan.VPCs,
		} {
			for _, item := range group {
				if item.Path == "" {
					continue
				}
				fmt.Fprintf(os.Stdout, "deleting %s (%s)\n", item.Label, item.Path)
				if _, err := linodeRequestRaw(token, "DELETE", item.Path, ""); err != nil {
					return err
				}
			}
		}
		if err := removeDestroyedLKEState(envRoot); err != nil {
			return err
		}
	}
	if includeObjectStorage {
		for _, item := range plan.ObjectBuckets {
			if item.Path == "" {
				return fmt.Errorf("cannot delete Object Storage bucket %q without a region", item.Label)
			}
			fmt.Fprintf(os.Stdout, "deleting %s (%s)\n", item.Label, item.Path)
			if _, err := linodeRequestRaw(token, "DELETE", item.Path, ""); err != nil {
				return err
			}
		}
	}
	if includeOrphanVolumes {
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
		}
	}
	return nil
}

func removeDestroyedLKEState(envRoot string) error {
	stateDir := filepath.Join(envRoot, "state")
	backupDir := filepath.Join(envRoot, "backups", "destroy-lke-"+time.Now().UTC().Format("20060102T150405Z"), "state")
	files := []string{"lke.env", "lke-kubeconfig.yaml"}
	backedUp := false
	for _, name := range files {
		src := filepath.Join(stateDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return err
		}
		if err := copyFile(src, filepath.Join(backupDir, name)); err != nil {
			return err
		}
		if err := os.Remove(src); err != nil {
			return err
		}
		backedUp = true
		fmt.Fprintf(os.Stdout, "removed local LKE state: %s\n", src)
	}
	if backedUp {
		fmt.Fprintf(os.Stdout, "local LKE state backup: %s\n", filepath.Dir(backupDir))
	}
	return nil
}
