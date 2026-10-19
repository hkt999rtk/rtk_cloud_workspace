package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// This explicit initialization action never runs from preflight or deployment.
// A partial or existing pair remains original state and is never replaced.
func initializeEnvironmentDeploymentSSH(store secretStore, confirm string) error {
	canonical, err := newSecretStore("", store.Environment)
	if err != nil || canonical.Root != store.Root || canonical.ConfigRoot != store.ConfigRoot || confirm != "video-cloud-"+store.Environment {
		return errors.New("deployment SSH initialization requires the selected canonical environment and exact stack confirmation")
	}
	if err := checkLocalExecutionFence(store.Root); err != nil {
		return err
	}
	for _, relative := range []string{"operator/ssh/id_ed25519", "operator/ssh/id_ed25519.pub"} {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil {
			return errors.New("selected deployment SSH identity permissions cannot be verified")
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("selected deployment SSH identity already exists or cannot be inspected; preserve its original key pair")
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("new selected deployment SSH identity cannot be generated")
	}
	encoded, err := ssh.MarshalPrivateKey(private, "rtk-cloud-"+store.Environment+"-deployment")
	if err != nil {
		return errors.New("new selected deployment SSH identity cannot be encoded")
	}
	authorized, err := ssh.NewPublicKey(public)
	if err != nil {
		return errors.New("new selected deployment SSH public key cannot be encoded")
	}
	for _, directory := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "operator"), filepath.Join(store.Root, "operator", "ssh")} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	if err := store.write("operator/ssh/id_ed25519", pem.EncodeToMemory(encoded), false); err != nil {
		return errors.New("new selected deployment SSH key cannot be published; preserve any partial original identity")
	}
	if err := store.write("operator/ssh/id_ed25519.pub", ssh.MarshalAuthorizedKey(authorized), false); err != nil {
		return errors.New("matching deployment SSH public key cannot be published; preserve the original private key for explicit recovery")
	}
	return nil
}
