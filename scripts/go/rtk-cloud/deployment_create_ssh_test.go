package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestCreateSSHInitializationRequiresExplicitNewCanonicalIdentity(t *testing.T) {
	for _, mode := range []string{"new", "existing", "partial", "foreign-confirm", "foreign-store", "nonprivate-directory", "active-execution"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			confirm := "video-cloud-dev"
			selected := store
			switch mode {
			case "existing":
				if err := initializeEnvironmentDeploymentSSH(store, confirm); err != nil {
					t.Fatal(err)
				}
			case "partial":
				if err := store.write("operator/ssh/id_ed25519", []byte("original-private-identity"), false); err != nil {
					t.Fatal(err)
				}
			case "foreign-confirm":
				confirm = "video-cloud-staging"
			case "foreign-store":
				selected.Root = filepath.Join(t.TempDir(), "dev")
			case "nonprivate-directory":
				if err := os.Mkdir(filepath.Join(store.Root, "operator", "ssh"), 0755); err != nil {
					t.Fatal(err)
				}
			case "active-execution":
				if err := store.write("deployment/.execution-active/owner.json", []byte("original-execution-owner"), false); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			err := initializeEnvironmentDeploymentSSH(selected, confirm)
			if mode != "new" {
				if err == nil || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
					t.Fatal("initialization replaced or repaired original identity", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			privatePath := filepath.Join(store.Root, "operator/ssh/id_ed25519")
			private, err := readBoundedEnvironmentFile(privatePath, true)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.ParsePrivateKey(private)
			if err != nil {
				t.Fatal(err)
			}
			publicPath := filepath.Join(store.Root, "operator/ssh/id_ed25519.pub")
			public, err := readBoundedEnvironmentFile(publicPath, true)
			if err != nil {
				t.Fatal(err)
			}
			plan := environmentCreateProviderPlan{SSHKeySHA256: environmentPreflightBytesSHA256([]byte(strings.TrimSpace(string(public))))}
			path, err := environmentCreateSSHIdentity(store, plan)
			if err != nil || path != privatePath || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
				t.Fatal("generated pair is not accepted by original deployment reader", err)
			}
			if _, err := os.Lstat(filepath.Join(store.Root, "kube/kubeconfig.yaml")); !os.IsNotExist(err) {
				t.Fatal("initial SSH action generated Kubernetes identity", err)
			}
		})
	}
}
