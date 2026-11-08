package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const generationRemovalProof = "deployment/remove-volume-ownership.json"
const generationArchivePending = "deployment/.generation-archive-active"

var generationArchivePaths = []string{
	"openbao", "pki", "runtime", "inventory.json", "kube",
	"deployment/adapters/lke/state.env", "deployment/adapters/lke/worker-labels.json", "deployment/identity-bindings.json",
	"deployment/account-bootstrap-retired.json", "deployment/create",
	"deployment/resources", "deployment/recovery", "deployment/rollback",
	"deployment/turn", "deployment/crl", "deployment/crl-maintenance",
	environmentCreateStorageAllocationPath, generationRemovalProof, generationRemovalIntent,
}

type deploymentGenerationArchive struct {
	Schema      int                          `json:"schema"`
	Environment string                       `json:"environment"`
	ClusterID   string                       `json:"original_cluster_id"`
	VolumeIDs   []string                     `json:"original_volume_ids"`
	InstanceIDs []string                     `json:"original_instance_ids,omitempty"`
	Entries     map[string]map[string]string `json:"entries"`
}

func runArchiveRemovedDeploymentGeneration(args []string) error {
	flags := flag.NewFlagSet("archive-removed-generation", flag.ContinueOnError)
	environment := flags.String("environment", "", "selected dev or staging environment")
	confirm := flags.String("confirm", "", "exact environment stack")
	clusterID := flags.String("original-cluster-id", "", "removed original numeric LKE cluster ID")
	workspace := flags.String("workspace", "", "workspace containing maintained environment settings")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*environment != "dev" && *environment != "staging") || *confirm != "video-cloud-"+*environment {
		return errors.New("select dev or staging and confirm its exact stack")
	}
	if _, err := positiveIntValue("original-cluster-id", *clusterID); err != nil {
		return err
	}
	if *workspace == "" {
		root, err := workspaceRoot()
		if err != nil {
			return err
		}
		*workspace = root
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	store, err := newSecretStore("", *environment)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := archiveRemovedDeploymentGeneration(ctx, cfg, store, *clusterID, verifyArchivedGenerationProviderAbsence); err != nil {
		return err
	}
	fmt.Printf("[VERIFIED] environment=%s original-cluster-id=%s private generation archive complete\n", *environment, *clusterID)
	return nil
}

func archiveRemovedDeploymentGeneration(ctx context.Context, cfg deploymentConfig, store secretStore, clusterID string, verifyAbsence func(context.Context, deploymentConfig, deploymentGenerationArchive) error) error {
	if cfg.Environment != store.Environment || cfg.Adapter != "lke" || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+store.Environment {
		return errors.New("selected archive store does not match the maintained environment")
	}
	if _, err := positiveIntValue("original cluster ID", clusterID); err != nil {
		return err
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := checkLocalExecutionFence(store.Root); err != nil {
		return err
	}
	pendingPath := filepath.Join(store.Root, generationArchivePending)
	_, pendingErr := os.Lstat(pendingPath)
	resuming := pendingErr == nil
	if resuming {
		pendingOwner, err := readBoundedEnvironmentFile(pendingPath, true)
		if err != nil || string(pendingOwner) != clusterID {
			return errors.New("another generation archive is incomplete or its owner cannot be verified")
		}
	} else if !errors.Is(pendingErr, os.ErrNotExist) {
		return pendingErr
	}
	base := filepath.Join("deployment", "archives", "lke-"+clusterID)
	manifestPath := filepath.Join(store.Root, base, "manifest.json")
	manifest := deploymentGenerationArchive{Schema: 1, Environment: cfg.Environment, ClusterID: clusterID, Entries: map[string]map[string]string{}}
	_, statErr := os.Lstat(manifestPath)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if fresh {
		proof, known, err := readDeploymentRemovalVolumes(cfg)
		if err != nil {
			return err
		}
		if !known || proof.ClusterID != clusterID {
			return errors.New("original cluster ID and exact removal volume evidence must match")
		}
		if err := validateDeploymentRemovalGeneration(cfg, proof, ""); err != nil {
			return err
		}
		manifest.VolumeIDs = proof.VolumeIDs
		intentPath := filepath.Join(store.Root, generationRemovalIntent)
		if _, err := os.Lstat(intentPath); err == nil {
			var intent originalGenerationRemoval
			targets, ledgerSHA, kubeSHA, loadErr := loadGenerationRemovalTargets(cfg, store, clusterID)
			if loadErr != nil {
				return loadErr
			}
			if readEnvironmentPreflightJSON(intentPath, &intent) != nil || intent.Schema != 1 || intent.Environment != cfg.Environment ||
				intent.ClusterID != clusterID || intent.LedgerSHA256 != ledgerSHA || intent.KubeSHA256 != kubeSHA ||
				intent.KubeSHA256 != proof.KubeSHA256 || !intent.KubernetesPurged || !slices.Equal(intent.VolumeIDs, proof.VolumeIDs) {
				return errors.New("original generation removal is incomplete or its exact ownership cannot be verified")
			}
			manifest.InstanceIDs = slices.Clone(intent.WorkerIDs)
			for _, target := range targets {
				if target.Kind == "instance" {
					manifest.InstanceIDs = append(manifest.InstanceIDs, target.NativeID)
				}
			}
			slices.Sort(manifest.InstanceIDs)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if statErr != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(manifestPath)) != nil ||
		readEnvironmentPreflightJSON(manifestPath, &manifest) != nil || manifest.Schema != 1 || manifest.Environment != cfg.Environment || manifest.ClusterID != clusterID || manifest.VolumeIDs == nil || manifest.Entries[generationRemovalProof]["."] == "" {
		return errors.New("existing generation archive manifest cannot be verified")
	}

	candidates := slices.Clone(generationArchivePaths)
	preflight := filepath.Join("deployment", "preflight")
	entries, err := os.ReadDir(filepath.Join(store.Root, preflight))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".publication.lock" {
			candidates = append(candidates, filepath.Join(preflight, entry.Name()))
		}
	}
	for relative := range manifest.Entries {
		if !slices.Contains(candidates, relative) {
			if filepath.Clean(relative) != relative || filepath.Dir(relative) != preflight || filepath.Base(relative) == ".publication.lock" {
				return errors.New("archive manifest contains an unsupported generation path")
			}
			candidates = append(candidates, relative)
		}
	}
	slices.Sort(candidates)

	// Validate the entire source/target inventory before moving anything.
	var pending []string
	for _, relative := range candidates {
		source := filepath.Join(store.Root, relative)
		target := filepath.Join(store.Root, base, "state", relative)
		current, err := hashGenerationArchiveEntry(store, source)
		if err != nil {
			return err
		}
		archived, err := hashGenerationArchiveEntry(store, target)
		if err != nil {
			return err
		}
		expected, known := manifest.Entries[relative]
		switch {
		case fresh && archived == nil && current != nil:
			manifest.Entries[relative] = current
			pending = append(pending, relative)
		case fresh && archived == nil:
		case !fresh && !known && current == nil && archived == nil:
		case !fresh && known && archived == nil && deploymentplan.Digest(current) == deploymentplan.Digest(expected):
			pending = append(pending, relative)
		case !fresh && known && current == nil && deploymentplan.Digest(archived) == deploymentplan.Digest(expected):
		default:
			return fmt.Errorf("generation archive source or destination changed at %s", relative)
		}
	}
	if err := verifyAbsence(ctx, cfg, manifest); err != nil {
		return err
	}
	if fresh {
		if err := prepareGenerationArchiveDirectory(store, filepath.Dir(manifestPath)); err != nil {
			return err
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		if err := store.write(filepath.Join(base, "manifest.json"), raw, false); err != nil {
			return err
		}
	}
	if len(pending) > 0 && !resuming {
		if err := store.write(generationArchivePending, []byte(clusterID), false); err != nil {
			return err
		}
		resuming = true
	}
	for _, relative := range pending {
		source := filepath.Join(store.Root, relative)
		target := filepath.Join(store.Root, base, "state", relative)
		if err := prepareGenerationArchiveDirectory(store, filepath.Dir(target)); err != nil {
			return err
		}
		current, err := hashGenerationArchiveEntry(store, source)
		if err != nil || deploymentplan.Digest(current) != deploymentplan.Digest(manifest.Entries[relative]) {
			return fmt.Errorf("generation archive source changed at %s", relative)
		}
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("generation archive destination appeared at %s", relative)
		}
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	if resuming {
		return os.Remove(pendingPath)
	}
	return nil
}

func checkGenerationArchiveIdle(store secretStore) error {
	if _, err := os.Lstat(filepath.Join(store.Root, generationArchivePending)); !errors.Is(err, os.ErrNotExist) {
		return environmentCreateBlocker("deployment.create.target", "previous-generation archival is incomplete or cannot be verified")
	}
	return nil
}

func prepareGenerationArchiveDirectory(store secretStore, path string) error {
	if path == store.Root {
		return validateEnvironmentPreflightDirectory(path)
	}
	if err := prepareGenerationArchiveDirectory(store, filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validateEnvironmentPreflightDirectory(path)
}

// The empty digest records a directory. Reject symlinks and special files.
func hashGenerationArchiveEntry(store secretStore, path string) (map[string]string, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := validateEnvironmentPreflightDirectories(store, filepath.Dir(path)); err != nil && filepath.Dir(path) != store.Root {
		return nil, err
	}
	hashes := map[string]string{}
	err := filepath.WalkDir(path, func(file string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, file)
		if err != nil {
			return err
		}
		info, err := os.Lstat(file)
		if err != nil {
			return err
		}
		if info.IsDir() && info.Mode().Perm() == 0700 {
			hashes[rel] = ""
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("generation archive requires private regular files and directories: %s", file)
		}
		fd, err := syscall.Open(file, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), file)
		defer f.Close()
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
			return errors.New("generation archive file changed during inspection")
		}
		digest := sha256.New()
		if n, err := io.Copy(digest, f); err != nil || n != info.Size() {
			return errors.New("generation archive could not read complete original bytes")
		}
		hashes[rel] = hex.EncodeToString(digest.Sum(nil))
		return nil
	})
	return hashes, err
}

func verifyArchivedGenerationProviderAbsence(ctx context.Context, cfg deploymentConfig, m deploymentGenerationArchive) error {
	token, err := dnsReconciliationToken(cfg)
	if err != nil {
		return err
	}
	checkAbsent := func(path string) error {
		_, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, path, nil)
		var status *environmentCreateProviderHTTPError
		if !errors.As(err, &status) || status.Status != http.StatusNotFound {
			return fmt.Errorf("native provider has not verified absence at %s", path)
		}
		return nil
	}
	if err := checkAbsent("/lke/clusters/" + m.ClusterID); err != nil {
		return err
	}
	for _, id := range m.VolumeIDs {
		if _, err := positiveIntValue("original volume ID", id); err != nil {
			return err
		}
		if err := checkAbsent("/volumes/" + id); err != nil {
			return err
		}
	}
	for _, id := range m.InstanceIDs {
		if _, err := positiveIntValue("original instance ID", id); err != nil {
			return err
		}
		if err := checkAbsent("/linode/instances/" + id); err != nil {
			return err
		}
	}
	values := environmentCreateProviderValues(cfg)
	match := linodeDestroyMatcher(values, values["CLOUD_STACK_NAME"], "")
	for _, group := range []string{"/lke/clusters", "/linode/instances", "/networking/firewalls", "/vpcs", "/nodebalancers", "/object-storage/buckets"} {
		items, err := environmentCreateProviderList(ctx, token, group)
		if err != nil {
			return fmt.Errorf("cannot verify complete provider inventory for %s: %w", group, err)
		}
		for _, raw := range items {
			var item struct {
				Label string   `json:"label"`
				Tags  []string `json:"tags"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			owned := match(item.Label, item.Tags)
			if group == "/object-storage/buckets" {
				owned = item.Label == cfg.Storage.RuntimeMedia.Bucket || item.Label == cfg.Storage.OTAFirmware.Bucket
			}
			if owned || strings.HasPrefix(item.Label, "lke"+m.ClusterID+"-") {
				return fmt.Errorf("provider resource %s in %s remains for the selected generation", item.Label, group)
			}
		}
	}
	return nil
}
