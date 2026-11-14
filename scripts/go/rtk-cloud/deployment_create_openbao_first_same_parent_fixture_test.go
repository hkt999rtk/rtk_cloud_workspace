package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// isolatedOpenBaoStageMaterial reads only the still-running synthetic parent and
// its actual native child plan/journal. The caller must separately obtain the
// objects from its simulated Kubernetes API; this function does not create Pod
// owners, resources, readiness, ACKs or journal entries.
func isolatedOpenBaoStageMaterial(cfg deploymentConfig, store secretStore, statefulset, secret, config deploymentplan.Object) (environmentCreateTransport, string, string, error) {
	var empty environmentCreateTransport
	bad := func(stage string) (environmentCreateTransport, string, string, error) {
		return empty, "", "", errors.New(stage)
	}
	if cfg.Environment != "dev" || store.Environment != cfg.Environment || !isolatedScriptTempPath(store.Root) {
		return bad("stage-environment")
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		return bad("stage-checkpoint")
	}
	original, configSHA, err := loadEnvironmentCreateAdmittedGeneration(store, checkpoint.Journal.PlanSHA256, checkpoint.Journal.ArtifactSHA256)
	if err != nil || !original.OpenBaoFirst || configSHA != deploymentplan.Digest(cfg) || original.Stack != cfg.Values["CLOUD_STACK_NAME"] {
		return bad("stage-admitted-plan")
	}
	var found *environmentCreateJournalEntry
	for i := range checkpoint.Journal.Entries {
		e := &checkpoint.Journal.Entries[i]
		if e.Key == "kubernetes/foundation" {
			if found != nil || (e.State != "pending" && e.State != "allocated" && e.State != "completed") {
				return bad("stage-foundation-entry")
			}
			found = e
		}
	}
	if found == nil {
		return bad("stage-foundation-absent")
	}
	parent := &environmentCreateExecution{Store: store, owner: checkpoint.Owner, Journal: checkpoint.Journal}
	child, journal, err := originalCreateChildAtIntent(store, parent, *found)
	if err != nil || child.Environment != cfg.Environment || child.Stack != original.Stack || child.ConfigurationSHA256 != original.ConfigurationSHA256 || child.ReleaseSHA256 != deploymentplan.Digest(original.Release) || journal.PlanSHA256 != child.SHA256() || journal.InFlight != "" || journal.Failed != "" {
		return bad("stage-child-journal")
	}
	ns := original.Stack + "-secrets"
	setID, secretID := "StatefulSet/"+ns+"/openbao", "Secret/"+ns+"/openbao-transport-tls"
	configID := "ConfigMap/" + ns + "/openbao-file"
	wanted := map[string]deploymentplan.Object{setID: statefulset, secretID: secret, configID: config}
	for _, id := range []string{setID, secretID, configID} {
		uid := deploymentplan.String(deploymentplan.Map(wanted[id]["metadata"])["uid"])
		if uid == "" || uid != journal.Owners[id] {
			return bad("stage-resource-owner")
		}
		matched := false
		for _, resource := range child.Resources {
			if resource.ID == id && resource.Action == "create" && deploymentplan.Matches(resource.Desired, wanted[id]) {
				matched = true
				break
			}
		}
		if !matched {
			return bad("stage-resource-desired")
		}
	}
	var material environmentCreateTransport
	path := filepath.Join(store.Root, "deployment/create/material", checkpoint.Journal.PlanSHA256, "openbao-transport/identities.json")
	if readEnvironmentPreflightJSON(path, &material) != nil || len(material.Identities) != 1 {
		return bad("stage-material-read")
	}
	identity, ok := material.Identities["openbao"]
	if !ok {
		return bad("stage-material-identity")
	}
	values := map[string]string{"tls.crt": identity.Certificate, "tls.key": identity.Key, "ca.crt": material.CACert}
	stringData, data := deploymentplan.Map(secret["stringData"]), deploymentplan.Map(secret["data"])
	for key, value := range values {
		if value == "" {
			return bad("stage-material-empty")
		}
		if stringData != nil {
			if deploymentplan.String(stringData[key]) != value {
				return bad("stage-material-secret-string")
			}
		} else {
			decoded, err := base64.StdEncoding.DecodeString(deploymentplan.String(data[key]))
			if err != nil || string(decoded) != value {
				return bad("stage-material-secret-data")
			}
		}
	}
	pod := environmentPKIPod(statefulset)
	containers := certIssuerObjectList(pod["containers"])
	if len(containers) != 1 || deploymentplan.String(deploymentplan.Map(containers[0])["image"]) != original.Release.Images["LKE_OPENBAO_IMAGE"].Reference || deploymentplan.String(deploymentplan.Map(containers[0])["name"]) != "openbao" {
		return bad("stage-container-recipe")
	}
	hcl := deploymentplan.String(deploymentplan.Map(config["data"])["file.hcl"])
	if hcl == "" {
		return bad("stage-config-hcl")
	}
	return material, journal.Owners[setID], hcl, nil
}

// launchIsolatedOpenBaoFromOriginalStage starts only the exact selected image
// with the transport and config already written by the original child stage.
// Kubernetes ownership remains a synthetic API observation; no Ready/ACK or
// journal result is fabricated by this test helper.
func launchIsolatedOpenBaoFromOriginalStage(ctx context.Context, cfg deploymentConfig, store secretStore, volumeParent string, statefulset, secret, config deploymentplan.Object) (string, string, string, func(), error) {
	fail := func(stage string) (string, string, string, func(), error) {
		return "", "", "", nil, errors.New(stage)
	}
	material, _, hcl, err := isolatedOpenBaoStageMaterial(cfg, store, statefulset, secret, config)
	if err != nil {
		return "", "", "", nil, err
	}
	identity := material.Identities["openbao"]
	pair, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.Key))
	if err != nil || len(pair.Certificate) != 1 {
		return fail("stage-tls-keypair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fail("stage-tls-cert")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(material.CACert)) {
		return fail("stage-tls-ca")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "openbao." + cfg.Values["CLOUD_STACK_NAME"] + "-secrets.svc", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fail("stage-tls-chain")
	}
	containers := certIssuerObjectList(environmentPKIPod(statefulset)["containers"])
	image := deploymentplan.String(deploymentplan.Map(containers[0])["image"])
	if !immutableEnvironmentImage.MatchString(image) {
		return fail("stage-image-reference")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return fail("stage-docker-path")
	}
	if !validIsolatedOpenBaoVolumeParent(volumeParent) {
		return fail("stage-volume-parent")
	}
	root, err := os.MkdirTemp(volumeParent, "rtk-original-stage-openbao-")
	if err != nil {
		return fail("stage-private-temp")
	}
	for _, name := range []string{"data", "audit"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			_ = os.RemoveAll(root)
			return fail("stage-private-volume")
		}
	}
	container := ""
	cleanup := func() {
		if container != "" {
			stop, done := context.WithTimeout(context.Background(), 15*time.Second)
			_ = exec.CommandContext(stop, docker, "rm", "--force", container).Run()
			done()
		}
		_ = os.RemoveAll(root)
	}
	for name, data := range map[string]string{"ca.crt": material.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key, "file.hcl": hcl} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			cleanup()
			return fail("stage-private-files")
		}
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		cleanup()
		return fail("stage-nonce")
	}
	owner := "rtk-original-bao-" + hex.EncodeToString(nonce[:])
	script := `mkdir -p /var/run/secrets/kubernetes.io/serviceaccount
cp /openbao/transport/ca.crt /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
exec bao server -config=/openbao/transport/file.hcl`
	start, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(start, docker, "run", "--detach", "--rm", "--pull=never", "--name", owner, "--label", "rtk.test.owner="+owner, "--network", "none", "--mount", "type=bind,src="+filepath.Join(root, "data")+",dst=/openbao/data", "--mount", "type=bind,src="+filepath.Join(root, "audit")+",dst=/openbao/audit", "--mount", "type=bind,src="+root+",dst=/openbao/transport,readonly", "--env", "BAO_CACERT=/openbao/transport/ca.crt", "--entrypoint", "/bin/sh", image, "-ec", script)
	out, err := cmd.Output()
	candidate := strings.TrimSpace(string(out))
	if err != nil || len(candidate) != 64 || strings.Trim(candidate, "0123456789abcdef") != "" {
		cleanup()
		return fail("stage-docker-start")
	}
	container = candidate
	return container, owner, root, cleanup, nil
}

func validIsolatedOpenBaoVolumeParent(path string) bool {
	if !isolatedScriptTempPath(path) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode().Perm() == 0700
}

func TestIsolatedOpenBaoVolumeParentRejectsWrongDirectory(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if !validIsolatedOpenBaoVolumeParent(parent) {
		t.Fatal("test-owned private parent rejected")
	}
	if validIsolatedOpenBaoVolumeParent(os.TempDir()) {
		t.Fatal("shared temp root accepted")
	}
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if validIsolatedOpenBaoVolumeParent(file) {
		t.Fatal("non-directory accepted")
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if validIsolatedOpenBaoVolumeParent(parent) {
		t.Fatal("public parent accepted")
	}
}

func TestIsolatedOpenBaoStageSourceRejectsMissingOriginalStage(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	if _, _, _, err := isolatedOpenBaoStageMaterial(cfg, store, nil, nil, nil); err == nil {
		t.Fatal("absent original foundation stage accepted")
	}
}
