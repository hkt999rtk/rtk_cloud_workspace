package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestroyLinodeStagingResourcesDryRunListsMatchesWithoutDeleting(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500": `{"data":[
			{"id":101,"label":"video-cloud-staging-lke","region":"us-sea"},
			{"id":999,"label":"ci-runner-lke","region":"us-sea"}
		]}`,
		"/linode/instances?page_size=500": `{"data":[
			{"id":201,"label":"video-cloud-staging-edge","region":"us-sea","status":"running","ipv4":["192.0.2.10"],"tags":["video-cloud-staging"]},
			{"id":203,"label":"video-cloud-staging-turn01","region":"us-sea","status":"running","ipv4":["192.0.2.20"],"tags":["rtk-cloud","video-cloud-staging","coturn-vm"]},
			{"id":202,"label":"home-100k-lg-1","region":"us-sea","status":"running","ipv4":["192.0.2.11"],"tags":["home-100k"]},
			{"id":999,"label":"ci-runner-1","region":"us-sea","status":"running","ipv4":["192.0.2.99"],"tags":["ci-runners"]}
		]}`,
		"/networking/firewalls?page_size=500": `{"data":[
			{"id":301,"label":"video-cloud-staging-edge"},
			{"id":999,"label":"ci-runner-fw"}
		]}`,
		"/vpcs?page_size=500": `{"data":[
			{"id":401,"label":"video-cloud-staging-vpc","region":"us-sea"},
			{"id":999,"label":"shared-vpc","region":"us-sea"}
		]}`,
		"/object-storage/buckets?page_size=500": `{"data":[
			{"label":"video-cloud-staging-artifacts","region":"us-sea"},
			{"label":"release-artifacts","region":"us-sea"}
		]}`,
		"/volumes?page_size=500": `{"data":[
			{"id":501,"label":"pvc-orphan","region":"us-sea","status":"active","linode_id":null},
			{"id":502,"label":"pvc-attached","region":"us-sea","status":"active","linode_id":201},
			{"id":503,"label":"manual-volume","region":"us-sea","status":"active","linode_id":null}
		]}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")

	var err error
	out := captureStdoutForDestroyTest(t, func() {
		err = run([]string{"destroy-linode-staging-resources", "--workspace", workspace, "--env-root", envRoot})
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"LKE clusters",
		"video-cloud-staging-lke",
		"Linode instances",
		"video-cloud-staging-edge",
		"video-cloud-staging-turn01",
		"home-100k-lg-1",
		"Firewalls",
		"video-cloud-staging-edge",
		"VPCs",
		"video-cloud-staging-vpc",
		"Object Storage buckets",
		"video-cloud-staging-artifacts",
		"Unattached pvc-* Block Storage volumes",
		"pvc-orphan",
		"dry-run only",
		"destroy video-cloud-staging",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected dry-run output to contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(readTestFile(t, curlLog), "DELETE ") {
		t.Fatalf("dry-run must not delete resources, got curl log:\n%s", readTestFile(t, curlLog))
	}
}

func TestDestroyLinodeStagingResourcesConfirmedDeletesMatchedResources(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	writeTestFile(t, filepath.Join(envRoot, "state", "lke.env"), "LKE_CLUSTER_ID=101\n")
	writeTestFile(t, filepath.Join(envRoot, "state", "lke-kubeconfig.yaml"), "apiVersion: v1\n")
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":           `{"data":[{"id":101,"label":"video-cloud-staging-lke","region":"us-sea"}]}`,
		"/linode/instances?page_size=500":       `{"data":[{"id":201,"label":"video-cloud-staging-edge","region":"us-sea","status":"running","ipv4":["192.0.2.10"],"tags":["video-cloud-staging"]},{"id":203,"label":"video-cloud-staging-turn01","region":"us-sea","status":"running","ipv4":["192.0.2.20"],"tags":["rtk-cloud","video-cloud-staging","coturn-vm"]}]}`,
		"/networking/firewalls?page_size=500":   `{"data":[{"id":301,"label":"video-cloud-staging-edge"}]}`,
		"/vpcs?page_size=500":                   `{"data":[{"id":401,"label":"video-cloud-staging-vpc","region":"us-sea"}]}`,
		"/object-storage/buckets?page_size=500": `{"data":[{"label":"video-cloud-staging-artifacts","region":"us-sea"}]}`,
		"/volumes?page_size=500":                `{"data":[{"id":501,"label":"pvc-orphan","region":"us-sea","status":"active","linode_id":null}]}`,
		"/lke/clusters/101":                     `{}`,
		"/linode/instances/201":                 `{}`,
		"/linode/instances/203":                 `{}`,
		"/networking/firewalls/301":             `{}`,
		"/vpcs/401":                             `{}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")

	err := run([]string{
		"destroy-linode-staging-resources",
		"--workspace", workspace,
		"--env-root", envRoot,
		"--yes",
		"--confirm-text", "destroy video-cloud-staging",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, curlLog)
	for _, want := range []string{
		"DELETE /lke/clusters/101",
		"DELETE /linode/instances/201",
		"DELETE /linode/instances/203",
		"DELETE /networking/firewalls/301",
		"DELETE /vpcs/401",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected curl log to contain %q, got:\n%s", want, log)
		}
	}
	if strings.Contains(log, "DELETE /object-storage/buckets") {
		t.Fatalf("object storage buckets must not be deleted without --include-object-storage, got:\n%s", log)
	}
	if strings.Contains(log, "DELETE /volumes") {
		t.Fatalf("orphan volumes must not be deleted without --include-orphan-volumes, got:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "state", "lke.env")); !os.IsNotExist(err) {
		t.Fatalf("expected local LKE state to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "state", "lke-kubeconfig.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected local LKE kubeconfig to be removed, stat err=%v", err)
	}
	backups, err := filepath.Glob(filepath.Join(envRoot, "backups", "destroy-lke-*", "state", "lke.env"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one local LKE state backup, got %v", backups)
	}
}

func TestDestroyLinodeStagingResourcesIncludesObjectStorageOnlyWhenRequested(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":                                  `{"data":[]}`,
		"/linode/instances?page_size=500":                              `{"data":[]}`,
		"/networking/firewalls?page_size=500":                          `{"data":[]}`,
		"/vpcs?page_size=500":                                          `{"data":[]}`,
		"/object-storage/buckets?page_size=500":                        `{"data":[{"label":"video-cloud-staging-artifacts","region":"us-sea"}]}`,
		"/volumes?page_size=500":                                       `{"data":[]}`,
		"/object-storage/buckets/us-sea/video-cloud-staging-artifacts": `{}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")

	err := run([]string{
		"destroy-linode-staging-resources",
		"--workspace", workspace,
		"--env-root", envRoot,
		"--yes",
		"--confirm-text", "destroy video-cloud-staging",
		"--include-object-storage",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, curlLog)
	if !strings.Contains(log, "DELETE /object-storage/buckets/us-sea/video-cloud-staging-artifacts") {
		t.Fatalf("expected object storage bucket delete, got:\n%s", log)
	}
}

func TestDestroyLinodeStagingResourcesIncludesOrphanVolumesOnlyWhenRequested(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":           `{"data":[]}`,
		"/linode/instances?page_size=500":       `{"data":[]}`,
		"/networking/firewalls?page_size=500":   `{"data":[]}`,
		"/vpcs?page_size=500":                   `{"data":[]}`,
		"/object-storage/buckets?page_size=500": `{"data":[]}`,
		"/volumes?page_size=500": `{"data":[
			{"id":501,"label":"pvc-orphan","region":"us-sea","status":"active","linode_id":null},
			{"id":502,"label":"pvc-attached","region":"us-sea","status":"active","linode_id":201},
			{"id":503,"label":"manual-volume","region":"us-sea","status":"active","linode_id":null}
		]}`,
		"/volumes/501": `{}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")

	err := run([]string{
		"destroy-linode-staging-resources",
		"--workspace", workspace,
		"--env-root", envRoot,
		"--yes",
		"--confirm-text", "destroy video-cloud-staging",
		"--include-orphan-volumes",
		"--orphan-volume-ids", "501",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, curlLog)
	if !strings.Contains(log, "DELETE /volumes/501") {
		t.Fatalf("expected orphan volume delete, got:\n%s", log)
	}
	if strings.Contains(log, "DELETE /volumes/502") || strings.Contains(log, "DELETE /volumes/503") {
		t.Fatalf("must not delete attached or non-pvc volumes, got:\n%s", log)
	}
}

func TestDestroyLinodeStagingResourcesOnlyOrphanVolumesSkipsRuntimeResources(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	writeTestFile(t, filepath.Join(envRoot, "state", "lke.env"), "LKE_CLUSTER_ID=101\n")
	writeTestFile(t, filepath.Join(envRoot, "state", "lke-kubeconfig.yaml"), "apiVersion: v1\n")
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":           `{"data":[{"id":101,"label":"video-cloud-staging-lke","region":"us-sea"}]}`,
		"/linode/instances?page_size=500":       `{"data":[{"id":201,"label":"video-cloud-staging-edge","region":"us-sea","status":"running","tags":["video-cloud-staging"]}]}`,
		"/networking/firewalls?page_size=500":   `{"data":[{"id":301,"label":"video-cloud-staging-edge"}]}`,
		"/vpcs?page_size=500":                   `{"data":[{"id":401,"label":"video-cloud-staging-vpc","region":"us-sea"}]}`,
		"/object-storage/buckets?page_size=500": `{"data":[{"label":"video-cloud-staging-artifacts","region":"us-sea"}]}`,
		"/volumes?page_size=500":                `{"data":[{"id":501,"label":"pvc-orphan","region":"us-sea","status":"active","linode_id":null}]}`,
		"/volumes/501":                          `{}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")

	var err error
	out := captureStdoutForDestroyTest(t, func() {
		err = run([]string{
			"destroy-linode-staging-resources",
			"--workspace", workspace,
			"--env-root", envRoot,
			"--yes",
			"--confirm-text", "destroy video-cloud-staging",
			"--only-orphan-volumes",
			"--include-orphan-volumes",
			"--orphan-volume-ids", "501",
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Only orphan volume mode is enabled") {
		t.Fatalf("expected only-orphan mode warning, got:\n%s", out)
	}
	log := readTestFile(t, curlLog)
	for _, forbidden := range []string{
		"DELETE /lke/clusters/101",
		"DELETE /linode/instances/201",
		"DELETE /networking/firewalls/301",
		"DELETE /vpcs/401",
		"DELETE /object-storage/buckets/us-sea/video-cloud-staging-artifacts",
	} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("only-orphan mode must not delete runtime resource %q, got:\n%s", forbidden, log)
		}
	}
	if !strings.Contains(log, "DELETE /volumes/501") {
		t.Fatalf("expected orphan volume delete, got:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "state", "lke.env")); err != nil {
		t.Fatalf("only-orphan mode must keep local LKE state, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "state", "lke-kubeconfig.yaml")); err != nil {
		t.Fatalf("only-orphan mode must keep local LKE kubeconfig, stat err=%v", err)
	}
}

func TestDestroyLinodeStagingResourcesRequiresExactOrphanVolumeIDs(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":           `{"data":[]}`,
		"/linode/instances?page_size=500":       `{"data":[]}`,
		"/networking/firewalls?page_size=500":   `{"data":[]}`,
		"/vpcs?page_size=500":                   `{"data":[]}`,
		"/object-storage/buckets?page_size=500": `{"data":[]}`,
		"/volumes?page_size=500":                `{"data":[{"id":501,"label":"pvc-orphan","region":"us-sea","status":"active","linode_id":null}]}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")

	err := run([]string{
		"destroy-linode-staging-resources",
		"--workspace", workspace,
		"--env-root", envRoot,
		"--yes",
		"--confirm-text", "destroy video-cloud-staging",
		"--include-orphan-volumes",
	})
	if err == nil || !strings.Contains(err.Error(), "--include-orphan-volumes requires --orphan-volume-ids") {
		t.Fatalf("expected exact id requirement, got %v", err)
	}
	if strings.Contains(readTestFile(t, curlLog), "DELETE /volumes") {
		t.Fatalf("must not delete orphan volumes without exact ids, got:\n%s", readTestFile(t, curlLog))
	}
}

func TestDestroyLinodeStagingResourcesRejectsWrongConfirmation(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	_ = fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":           `{"data":[]}`,
		"/linode/instances?page_size=500":       `{"data":[]}`,
		"/networking/firewalls?page_size=500":   `{"data":[]}`,
		"/vpcs?page_size=500":                   `{"data":[]}`,
		"/object-storage/buckets?page_size=500": `{"data":[]}`,
		"/volumes?page_size=500":                `{"data":[]}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")

	err := run([]string{
		"destroy-linode-staging-resources",
		"--workspace", workspace,
		"--env-root", envRoot,
		"--yes",
		"--confirm-text", "destroy wrong-stack",
	})
	if err == nil || !strings.Contains(err.Error(), "confirmation text must be") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
}

func captureStdoutForDestroyTest(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = old }()

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, reader)
		done <- buf.String()
	}()

	fn()
	_ = writer.Close()
	return <-done
}

func TestDestroyLinodeStagingShellWrapperExists(t *testing.T) {
	path := filepath.Join("..", "..", "destroy-linode-staging-resources.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "destroy-linode-staging-resources") {
		t.Fatalf("wrapper should invoke destroy-linode-staging-resources, got:\n%s", string(data))
	}
}
