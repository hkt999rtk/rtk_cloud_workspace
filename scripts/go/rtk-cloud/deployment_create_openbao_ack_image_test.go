package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type createOpenBaoACKImageFixture struct {
	store       secretStore
	cfg         deploymentConfig
	parent      string
	selection   createOpenBaoACKImageSelection
	manifest    map[string]any
	publication []byte
	runInfo     map[string]any
	commandErr  error
	calls       int
}

func newCreateOpenBaoACKImageFixture(t *testing.T) *createOpenBaoACKImageFixture {
	t.Helper()
	f := &createOpenBaoACKImageFixture{store: makeIsolatedTestSecretStore(t, "dev"), cfg: deploymentConfig{Environment: "dev"}, parent: strings.Repeat("a", 64)}
	f.selection = createOpenBaoACKImageSelection{
		Schema: "rtk-cloud.create-openbao-host-ack-selection/v1", Parent: f.parent,
		Role: "LKE_OPENBAO_PKI_IMAGE", Revision: strings.Repeat("b", 40), RunID: 77, Attempt: 2,
	}
	f.manifest = map[string]any{
		"schema": "rtk-service-image/v1", "repository": "hkt999rtk/rtk_video_cloud",
		"source_commit": f.selection.Revision, "run_id": "77", "run_attempt": "2",
		"image": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", "digest": "sha256:" + strings.Repeat("c", 64),
		"images": map[string]any{"openbao_pki": map[string]any{
			"image": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki", "digest": "sha256:" + strings.Repeat("d", 64),
		}},
	}
	f.runInfo = map[string]any{"id": 77, "head_sha": f.selection.Revision, "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success", "run_attempt": 2, "path": ".github/workflows/release.yml"}
	f.write(t)
	return f
}

func (f *createOpenBaoACKImageFixture) manifestPath() string {
	return filepath.Join("deployment/repairs/openbao-host-ack", f.selection.Revision, "image-manifest.json")
}

func (f *createOpenBaoACKImageFixture) write(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	f.publication = raw
	f.selection.ManifestSHA = environmentPreflightBytesSHA256(raw)
	if err = f.store.write(f.manifestPath(), raw, true); err != nil {
		t.Fatal(err)
	}
	f.writeSelection(t)
}

func (f *createOpenBaoACKImageFixture) writeSelection(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(f.selection)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.write(createOpenBaoACKImageSelectionPath, raw, true); err != nil {
		t.Fatal(err)
	}
}

func (f *createOpenBaoACKImageFixture) command(t *testing.T) environmentReleaseCommand {
	t.Helper()
	archive := publicationZIP(t, "image-manifest.json", f.publication)
	return func(ctx context.Context, command string, args ...string) ([]byte, error) {
		f.calls++
		if command != "gh" || len(args) != 2 || args[0] != "api" {
			t.Fatal("repair loader escaped read-only exact GitHub artifact commands")
		}
		if f.commandErr != nil {
			return nil, f.commandErr
		}
		switch args[1] {
		case "repos/hkt999rtk/rtk_video_cloud/actions/runs/77":
			return json.Marshal(f.runInfo)
		case "repos/hkt999rtk/rtk_video_cloud/actions/runs/77/artifacts?per_page=100":
			return json.Marshal(map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 99, "name": "image-manifest", "size_in_bytes": len(archive), "expired": false}}})
		case "repos/hkt999rtk/rtk_video_cloud/actions/artifacts/99/zip":
			return archive, nil
		default:
			t.Fatal("repair loader selected another publication or repository")
			return nil, nil
		}
	}
}

func createOpenBaoACKStoreSnapshot(t *testing.T, store secretStore) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(store.ConfigRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += environmentPreflightBytesSHA256(raw)
		}
		result[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCreateOpenBaoACKRepairImageBindsRolePublicationAndNeverWrites(t *testing.T) {
	f := newCreateOpenBaoACKImageFixture(t)
	before := createOpenBaoACKStoreSnapshot(t, f.store)
	image, selectionSHA, manifestSHA, err := loadCreateOpenBaoACKRepairImageWithCommand(context.Background(), f.cfg, f.store, f.parent, f.command(t))
	selection, readErr := os.ReadFile(filepath.Join(f.store.Root, createOpenBaoACKImageSelectionPath))
	if err != nil || readErr != nil || image.Reference != "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:"+strings.Repeat("d", 64) || image.SourceRevision != f.selection.Revision || image.OTARegistration != nil || selectionSHA != environmentPreflightBytesSHA256(selection) || manifestSHA != f.selection.ManifestSHA || f.calls != 3 {
		t.Fatalf("exact OpenBao role publication not bound: %v", err)
	}
	if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.store)) {
		t.Fatal("role proof reader wrote environment state")
	}
	// A rerun invalidates the earlier selection even within the same invocation.
	f.runInfo["run_attempt"] = 3
	image, selectionSHA, manifestSHA, err = loadCreateOpenBaoACKRepairImageWithCommand(context.Background(), f.cfg, f.store, f.parent, f.command(t))
	if err == nil || image != (environmentReleaseImage{}) || selectionSHA != "" || manifestSHA != "" || f.calls != 6 {
		t.Fatal("reader reused an earlier successful attempt")
	}
}

func TestCreateOpenBaoACKRepairImageRejectsLocalSelectionAndCustodyDrift(t *testing.T) {
	for _, mode := range []string{"schema", "parent", "role", "revision-path", "run", "attempt", "hash", "environment", "unknown", "trailing", "selection-missing", "manifest-missing", "manifest-bytes", "selection-permissions", "manifest-permissions", "parent-permissions", "selection-parent-symlink", "manifest-parent-symlink", "manifest-symlink"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreateOpenBaoACKImageFixture(t)
			selectionPath := filepath.Join(f.store.Root, createOpenBaoACKImageSelectionPath)
			manifestPath := filepath.Join(f.store.Root, f.manifestPath())
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "schema":
				f.selection.Schema = "other/v1"
				f.writeSelection(t)
			case "parent":
				f.selection.Parent = strings.Repeat("c", 64)
				f.writeSelection(t)
			case "role":
				f.selection.Role = "LKE_VIDEO_CLOUD_IMAGE"
				f.writeSelection(t)
			case "revision-path":
				f.selection.Revision = "../../other"
				f.writeSelection(t)
			case "run":
				f.selection.RunID = 0
				f.writeSelection(t)
			case "attempt":
				f.selection.Attempt = 0
				f.writeSelection(t)
			case "hash":
				f.selection.ManifestSHA = "invalid"
				f.writeSelection(t)
			case "environment":
				f.cfg.Environment = "prod"
			case "unknown", "trailing":
				raw, err := os.ReadFile(selectionPath)
				must(err)
				if mode == "unknown" {
					raw = append(raw[:len(raw)-1], []byte(`,"credential":"never-display"}`)...)
				} else {
					raw = append(raw, []byte(` {}`)...)
				}
				must(os.WriteFile(selectionPath, raw, 0600))
			case "selection-missing":
				must(os.Remove(selectionPath))
			case "manifest-missing":
				must(os.Remove(manifestPath))
			case "manifest-bytes":
				must(os.WriteFile(manifestPath, append(f.publication, '\n'), 0600))
			case "selection-permissions":
				must(os.Chmod(selectionPath, 0644))
			case "manifest-permissions":
				must(os.Chmod(manifestPath, 0644))
			case "parent-permissions":
				must(os.Chmod(filepath.Dir(selectionPath), 0755))
			case "selection-parent-symlink", "manifest-parent-symlink":
				dir := filepath.Dir(selectionPath)
				if mode == "manifest-parent-symlink" {
					dir = filepath.Dir(manifestPath)
				}
				must(os.Rename(dir, dir+"-retained"))
				must(os.Symlink(dir+"-retained", dir))
			case "manifest-symlink":
				must(os.Rename(manifestPath, manifestPath+"-retained"))
				must(os.Symlink(manifestPath+"-retained", manifestPath))
			}
			before := createOpenBaoACKStoreSnapshot(t, f.store)
			image, selectionSHA, manifestSHA, err := loadCreateOpenBaoACKRepairImageWithCommand(context.Background(), f.cfg, f.store, f.parent, f.command(t))
			if err == nil || image != (environmentReleaseImage{}) || selectionSHA != "" || manifestSHA != "" || f.calls != 0 || strings.Contains(err.Error(), "never-display") {
				t.Fatal("local drift escaped the pre-publication guard")
			}
			if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.store)) {
				t.Fatal("failed reader wrote environment state")
			}
		})
	}
}

func TestCreateOpenBaoACKRepairImageRejectsPublicationDrift(t *testing.T) {
	for _, mode := range []string{"different-workflow", "failed", "branch", "source", "current-attempt", "different-actual-bytes", "artifact-source", "artifact-run", "artifact-attempt", "missing-target", "mutable-target", "conflicting-target", "private-command-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreateOpenBaoACKImageFixture(t)
			switch mode {
			case "different-workflow":
				f.runInfo["path"] = ".github/workflows/ci.yml"
			case "failed":
				f.runInfo["conclusion"] = "failure"
			case "branch":
				f.runInfo["head_branch"] = "feature"
			case "source":
				f.runInfo["head_sha"] = strings.Repeat("c", 40)
			case "current-attempt":
				f.runInfo["run_attempt"] = 3
			case "different-actual-bytes":
				f.publication = append(f.publication, '\n')
			case "artifact-source":
				f.manifest["source_commit"] = strings.Repeat("c", 40)
				f.write(t)
			case "artifact-run":
				f.manifest["run_id"] = "78"
				f.write(t)
			case "artifact-attempt":
				f.manifest["run_attempt"] = "1"
				f.write(t)
			case "missing-target":
				delete(f.manifest, "images")
				f.write(t)
			case "mutable-target":
				f.manifest["images"].(map[string]any)["openbao_pki"].(map[string]any)["digest"] = "latest"
				f.write(t)
			case "conflicting-target":
				f.manifest["images"].(map[string]any)["other"] = map[string]any{"image": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki", "digest": "sha256:" + strings.Repeat("e", 64)}
				f.write(t)
			case "private-command-error":
				f.commandErr = errors.New("credential=never-display")
			}
			before := createOpenBaoACKStoreSnapshot(t, f.store)
			image, selectionSHA, manifestSHA, err := loadCreateOpenBaoACKRepairImageWithCommand(context.Background(), f.cfg, f.store, f.parent, f.command(t))
			if err == nil || image != (environmentReleaseImage{}) || selectionSHA != "" || manifestSHA != "" || strings.Contains(err.Error(), "never-display") {
				t.Fatal("publication drift returned usable repair evidence")
			}
			if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.store)) {
				t.Fatal("publication reader wrote environment state")
			}
		})
	}
}
