package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func prepareCertIssuerPublicationFixture(t *testing.T) (*createOpenBaoACKImageFixture, createCertIssuerPrepareImageSelection) {
	t.Helper()
	f := newCreateOpenBaoACKImageFixture(t)
	s := createCertIssuerPrepareImageSelection{Schema: "rtk-cloud.create-certissuer-provider-prepare-selection/v1", Parent: f.parent, Role: "LKE_CERTISSUER_IMAGE", Revision: f.selection.Revision, RunID: 77, Attempt: 2, ManifestSHA: f.selection.ManifestSHA}
	writeCertIssuerPublicationFixture(t, f, s)
	return f, s
}
func writeCertIssuerPublicationFixture(t *testing.T, f *createOpenBaoACKImageFixture, s createCertIssuerPrepareImageSelection) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.write(createCertIssuerPrepareImageSelectionPath, raw, true); err != nil {
		t.Fatal(err)
	}
	if err = f.store.write(filepath.Join("deployment/repairs/certissuer-provider-prepare", s.Revision, "image-manifest.json"), f.publication, true); err != nil {
		t.Fatal(err)
	}
}
func TestCreateCertIssuerPreparePublicationPinsOnlyAPIImageAndNeverWrites(t *testing.T) {
	f, _ := prepareCertIssuerPublicationFixture(t)
	before := createOpenBaoACKStoreSnapshot(t, f.store)
	image, selection, manifest, err := loadCreateCertIssuerPrepareRepairImageWithCommand(context.Background(), f.cfg, f.store, f.parent, f.command(t))
	if err != nil || image.Reference != "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:"+strings.Repeat("c", 64) || image.SourceRevision != f.selection.Revision || selection == "" || manifest != f.selection.ManifestSHA || f.calls != 3 {
		t.Fatalf("formal CertIssuer API publication: %v", err)
	}
	if !reflect.DeepEqual(before, createOpenBaoACKStoreSnapshot(t, f.store)) {
		t.Fatal("proof reader changed private state")
	}
	f.runInfo["run_attempt"] = 3
	if _, _, _, err = loadCreateCertIssuerPrepareRepairImageWithCommand(context.Background(), f.cfg, f.store, f.parent, f.command(t)); err == nil {
		t.Fatal("accepted superseded CI attempt")
	}
}
func TestCreateCertIssuerPreparePublicationRejectsWrongSourceAuthority(t *testing.T) {
	for _, mode := range []string{"parent", "role", "schema", "manifest", "permission", "workflow", "revision", "branch", "conclusion"} {
		t.Run(mode, func(t *testing.T) {
			f, s := prepareCertIssuerPublicationFixture(t)
			switch mode {
			case "parent":
				s.Parent = strings.Repeat("e", 64)
			case "role":
				s.Role = "LKE_VIDEO_CLOUD_IMAGE"
			case "schema":
				s.Schema = "rtk-cloud.create-openbao-host-ack-selection/v1"
			case "manifest":
				s.ManifestSHA = strings.Repeat("e", 64)
			case "permission":
				if err := os.Chmod(filepath.Join(f.store.Root, createCertIssuerPrepareImageSelectionPath), 0644); err != nil {
					t.Fatal(err)
				}
			case "workflow":
				f.runInfo["path"] = ".github/workflows/adhoc.yml"
			case "revision":
				f.runInfo["head_sha"] = strings.Repeat("e", 40)
			case "branch":
				f.runInfo["head_branch"] = "candidate"
			case "conclusion":
				f.runInfo["conclusion"] = "failure"
			}
			if mode != "permission" {
				writeCertIssuerPublicationFixture(t, f, s)
			}
			if _, _, _, err := loadCreateCertIssuerPrepareRepairImageWithCommand(context.Background(), f.cfg, f.store, f.parent, f.command(t)); err == nil {
				t.Fatal("accepted wrong formal authority")
			}
		})
	}
}
