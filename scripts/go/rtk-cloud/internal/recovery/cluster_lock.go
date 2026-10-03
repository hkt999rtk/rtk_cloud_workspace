package recovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"
)

type clusterCommand func(context.Context, io.Reader, io.Writer, ...string) error

// AcquireClusterLock coordinates scheduled PostgreSQL backups and core recovery.
// An empty kubeconfig is exclusively in-cluster mode; it never uses a controller's
// default kubeconfig. Locks have no automatic expiry: a killed owner is checked by
// an operator before its lock is removed.
func AcquireClusterLock(ctx context.Context, kubectl, kubeconfig, namespace, owner string, rejectMaintenance bool) (func() error, error) {
	if kubectl == "" {
		kubectl = "kubectl"
	}
	cleanup := func() {}
	if kubeconfig == "" {
		var err error
		kubeconfig, cleanup, err = serviceAccountConfig()
		if err != nil {
			return nil, err
		}
	}
	kube := func(ctx context.Context, in io.Reader, out io.Writer, args ...string) error {
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return QuietExec(call, append([]string{kubectl, "--kubeconfig", kubeconfig, "--request-timeout=30s"}, args...), in, out)
	}
	release, err := acquireClusterLock(ctx, kube, namespace, owner, rejectMaintenance)
	if err != nil {
		cleanup()
		return nil, err
	}
	return func() error { defer cleanup(); return release() }, nil
}

// ServiceAccountConfig creates an explicit token-file-based client configuration
// for the current Pod. The caller must invoke cleanup after all API operations.
func ServiceAccountConfig() (string, func(), error) { return serviceAccountConfig() }

func serviceAccountConfig() (string, func(), error) {
	const base = "/var/run/secrets/kubernetes.io/serviceaccount/"
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return "", nil, errors.New("explicit kubeconfig or in-cluster service account required")
	}
	for _, name := range []string{"token", "ca.crt"} {
		if info, err := os.Stat(base + name); err != nil || !info.Mode().IsRegular() {
			return "", nil, errors.New("in-cluster service account unavailable")
		}
	}
	config := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "backup", "clusters": []any{map[string]any{"name": "cluster", "cluster": map[string]string{"server": "https://" + net.JoinHostPort(host, port), "certificate-authority": base + "ca.crt"}}}, "users": []any{map[string]any{"name": "backup", "user": map[string]string{"tokenFile": base + "token"}}}, "contexts": []any{map[string]any{"name": "backup", "context": map[string]string{"cluster": "cluster", "user": "backup"}}}}
	f, err := os.CreateTemp("", "rtk-backup-kubeconfig-*")
	if err != nil {
		return "", nil, errors.New("cannot prepare in-cluster client")
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	err = json.NewEncoder(f).Encode(config)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		cleanup()
		return "", nil, errors.New("cannot prepare in-cluster client")
	}
	return f.Name(), cleanup, nil
}

func acquireClusterLock(ctx context.Context, kube clusterCommand, namespace, owner string, rejectMaintenance bool) (func() error, error) {
	if !Name.MatchString(namespace) || owner == "" || len(owner) > 256 {
		return nil, errors.New("invalid cluster lock identity")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(nonce)
	object := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": lockName + "-command", "namespace": namespace}, "data": map[string]string{"owner": owner, "token": token}}
	b, _ := json.Marshal(object)
	var created limitedBuffer
	created.limit = 16 << 10
	if err := kube(ctx, bytes.NewReader(b), &created, "-n", namespace, "create", "-f", "-", "-o", "json"); err != nil {
		return nil, errors.New("cluster recovery command lock exists/unavailable; concurrent operations refused")
	}
	var cm struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(created.Bytes(), &cm) != nil || cm.Metadata.UID == "" || cm.Data["token"] != token {
		return nil, errors.New("cluster lock creation unverified; inspect owner before removing lock")
	}
	uid := cm.Metadata.UID
	released := false
	release := func() error {
		if released {
			return nil
		}
		call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		options, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": uid}})
		if err := kube(call, bytes.NewReader(options), io.Discard, "delete", "--raw", "/api/v1/namespaces/"+namespace+"/configmaps/"+lockName+"-command", "-f", "-"); err != nil {
			return errors.New("cluster lock cleanup failed; confirm owner stopped before removing its lock")
		}
		released = true
		return nil
	}
	if rejectMaintenance {
		var state limitedBuffer
		state.limit = 1 << 20
		err := kube(ctx, nil, &state, "-n", namespace, "get", "configmap", lockName, "--ignore-not-found=true", "-o", "json")
		if err != nil || len(bytes.TrimSpace(state.Bytes())) != 0 {
			cleanupErr := release()
			if cleanupErr != nil {
				return nil, cleanupErr
			}
			return nil, errors.New("core maintenance active or state unavailable; online backup refused")
		}
	}
	return release, nil
}
