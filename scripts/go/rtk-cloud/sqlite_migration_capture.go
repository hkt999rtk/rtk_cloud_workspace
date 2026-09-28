package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var sqliteCaptureStackName = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

type sqliteCaptureDeployment struct {
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Volumes []struct {
					Name string `json:"name"`
				} `json:"volumes"`
				Containers []struct {
					Name         string `json:"name"`
					VolumeMounts []struct {
						MountPath string `json:"mountPath"`
					} `json:"volumeMounts"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

type sqliteCapturePods struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	} `json:"items"`
}

func runSQLiteMigrationCapture(args []string) error {
	fs := flag.NewFlagSet("sqlite-migration-capture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stack := fs.String("stack", "", "selected Kubernetes stack")
	kubeconfig := fs.String("kubeconfig", "", "selected kubeconfig")
	workload := fs.String("workload", "", "cloud-admin or frontend")
	uid := fs.String("source-pod-uid", "", "UID recorded at the maintenance fence")
	output := fs.String("output-dir", "", "existing empty private 0700 directory")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("invalid sqlite-migration-capture arguments")
	}
	if !sqliteCaptureStackName.MatchString(*stack) || !validSQLitePodUID(*uid) {
		return errors.New("valid --stack and --source-pod-uid required")
	}
	if *workload != "cloud-admin" && *workload != "frontend" {
		return errors.New("--workload must be cloud-admin or frontend")
	}
	if info, err := os.Stat(*kubeconfig); err != nil || !info.Mode().IsRegular() {
		return errors.New("--kubeconfig must be an existing regular file")
	}
	if err := sqliteMigrationPrivateDir(*output); err != nil {
		return fmt.Errorf("output directory: %w", err)
	}
	entries, err := os.ReadDir(*output)
	if err != nil || len(entries) != 0 {
		return errors.New("output directory must be empty")
	}
	namespace, deployment, directory := sqliteCaptureTarget(*stack, *workload)
	client := sqliteCaptureClient{Kubeconfig: *kubeconfig, Namespace: namespace}
	if err := client.checkDeployment(deployment, directory); err != nil {
		return err
	}
	pod, err := client.sourcePod(deployment, *uid)
	if err != nil {
		return err
	}
	before, err := client.sourceHashes(pod, directory)
	if err != nil {
		return err
	}
	if err := sqliteCaptureRequired(*workload, before); err != nil {
		return err
	}
	if err := client.copyTar(pod, directory, *output, before); err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			for name := range before {
				_ = os.Remove(filepath.Join(*output, name))
			}
		}
	}()
	if _, err := client.sourcePod(deployment, *uid); err != nil {
		return err
	}
	after, err := client.sourceHashes(pod, directory)
	if err != nil || !sqliteCaptureHashesEqual(before, after) {
		return errors.New("source SQLite file set changed during capture")
	}
	if _, err := sqliteMigrationInspect(*output, *workload, *uid); err != nil {
		return fmt.Errorf("captured SQLite files: %w", err)
	}
	completed = true
	for _, name := range sqliteCaptureNames(before) {
		fmt.Printf("%s  %s\n", before[name], name)
	}
	fmt.Printf("Source Pod UID: %s\nPrivate copy: %s\n", *uid, *output)
	return nil
}

func sqliteCaptureTarget(stack, workload string) (string, string, string) {
	if workload == "cloud-admin" {
		return stack + "-admin", "cloud-admin", "/app/data"
	}
	return stack + "-frontend", "frontend", "/data"
}

type sqliteCaptureClient struct{ Kubeconfig, Namespace string }

func (c sqliteCaptureClient) command(args ...string) *exec.Cmd {
	return exec.Command("kubectl", append([]string{"--kubeconfig", c.Kubeconfig, "-n", c.Namespace}, args...)...)
}

func (c sqliteCaptureClient) output(args ...string) ([]byte, error) {
	out, err := c.command(args...).Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl source check failed: %w", err)
	}
	return out, nil
}

func (c sqliteCaptureClient) checkDeployment(name, directory string) error {
	out, err := c.output("get", "deployment", name, "-o", "json")
	if err != nil {
		return err
	}
	var d sqliteCaptureDeployment
	if err := json.Unmarshal(out, &d); err != nil {
		return err
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
		return errors.New("source Deployment must have exactly one replica")
	}
	for _, volume := range d.Spec.Template.Spec.Volumes {
		if volume.Name == "sqlite-data" {
			return errors.New("source Deployment already has SQLite data volume")
		}
	}
	for _, container := range d.Spec.Template.Spec.Containers {
		if container.Name == "app" {
			for _, mount := range container.VolumeMounts {
				if mount.MountPath == directory {
					return errors.New("source Deployment already has data mount")
				}
			}
		}
	}
	return nil
}

func (c sqliteCaptureClient) sourcePod(deployment, uid string) (string, error) {
	out, err := c.output("get", "pods", "-l", "app.kubernetes.io/name="+deployment, "-o", "json")
	if err != nil {
		return "", err
	}
	var pods sqliteCapturePods
	if err := json.Unmarshal(out, &pods); err != nil {
		return "", err
	}
	if len(pods.Items) != 1 || pods.Items[0].Metadata.Name == "" || pods.Items[0].Metadata.UID != uid || pods.Items[0].Status.Phase != "Running" {
		return "", errors.New("source Pod UID, count or Running state changed")
	}
	return pods.Items[0].Metadata.Name, nil
}

func (c sqliteCaptureClient) remote(pod, script, directory string, names ...string) ([]byte, error) {
	args := []string{"exec", pod, "-c", "app", "--", "sh", "-c", script, "sh", directory}
	args = append(args, names...)
	return c.output(args...)
}

func (c sqliteCaptureClient) sourceHashes(pod, directory string) (map[string]string, error) {
	const listScript = `set -eu; find "$1" -maxdepth 1 -type f \( -name '*.db' -o -name '*.db-*' \) -print`
	out, err := c.remote(pod, listScript, directory)
	if err != nil {
		return nil, err
	}
	paths := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(paths) == 1 && paths[0] == "" {
		return nil, errors.New("no source SQLite files")
	}
	names := make([]string, 0, len(paths))
	seen := make(map[string]bool)
	for _, path := range paths {
		name := filepath.Base(path)
		if filepath.Dir(path) != directory || !sqliteMigrationName.MatchString(name) || seen[name] {
			return nil, errors.New("unexpected source SQLite filename")
		}
		seen[name] = true
		names = append(names, name)
	}
	slices.Sort(names)
	const hashScript = `set -eu; cd "$1"; shift; for name do sha256sum "$name"; done`
	out, err = c.remote(pod, hashScript, directory, names...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(names) {
		return nil, errors.New("source hash count mismatch")
	}
	hashes := make(map[string]string, len(names))
	for i, line := range lines {
		if len(line) != 66+len(names[i]) || line[64:] != "  "+names[i] {
			return nil, errors.New("invalid source hash output")
		}
		if _, err := hex.DecodeString(line[:64]); err != nil {
			return nil, errors.New("invalid source SHA-256")
		}
		hashes[names[i]] = strings.ToLower(line[:64])
	}
	return hashes, nil
}

func sqliteCaptureRequired(workload string, hashes map[string]string) error {
	required := []string{"rtk-cloud-admin.db"}
	if workload == "frontend" {
		required = []string{"connectplus.db", "analytics.db"}
	}
	for _, name := range required {
		if _, ok := hashes[name]; !ok {
			return fmt.Errorf("required SQLite source %s is missing", name)
		}
	}
	return nil
}

func sqliteCaptureNames(hashes map[string]string) []string {
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func sqliteCaptureHashesEqual(a, b map[string]string) bool {
	return len(a) == len(b) && slices.EqualFunc(sqliteCaptureNames(a), sqliteCaptureNames(b), func(x, y string) bool { return x == y && a[x] == b[y] })
}

func (c sqliteCaptureClient) copyTar(pod, directory, output string, hashes map[string]string) error {
	names := sqliteCaptureNames(hashes)
	args := []string{"exec", pod, "-c", "app", "--", "sh", "-c", `set -eu; cd "$1"; shift; tar -cf - "$@"`, "sh", directory}
	args = append(args, names...)
	cmd := c.command(args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start source tar: %w", err)
	}
	seen := make(map[string]bool, len(names))
	reader := tar.NewReader(pipe)
	var copyErr error
	for copyErr == nil {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			copyErr = err
			break
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			copyErr = errors.New("source tar contains non-regular entry")
			break
		}
		if _, ok := hashes[header.Name]; !ok || seen[header.Name] || header.Size <= 0 {
			copyErr = fmt.Errorf("source tar contains unexpected entry %q size=%d", header.Name, header.Size)
			break
		}
		seen[header.Name] = true
		path := filepath.Join(output, header.Name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			copyErr = err
			break
		}
		hash := sha256.New()
		_, copyErr = io.Copy(io.MultiWriter(file, hash), reader)
		closeErr := file.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != hashes[header.Name] {
			copyErr = errors.New("captured SQLite hash differs from source")
		}
	}
	if copyErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if copyErr != nil || waitErr != nil || len(seen) != len(names) {
		for _, name := range names {
			_ = os.Remove(filepath.Join(output, name))
		}
		return fmt.Errorf("source SQLite tar capture failed or hashes differ: copy=%v process=%v entries=%d/%d", copyErr, waitErr, len(seen), len(names))
	}
	return nil
}
