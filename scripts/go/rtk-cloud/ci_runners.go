package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/runner"
)

func runCollectEvidence(args []string) error {
	return runLogsCheck(args)
}

func runCIRunnersProvision(args []string) error {
	fs := flag.NewFlagSet("ci-runners provision", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return provisionCIRunnerHosts()
}

func runCIRunnersArchiveArtifacts(args []string) error {
	return archiveCIRunnerArtifacts(args)
}

func runCIRunnersRunSession(args []string) error {
	return runCIRunnerSession(args)
}

func provisionCIRunnerHosts() error {
	if os.Getenv("LINODE_TOKEN") == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	publicKeyPath := firstNonEmpty(os.Getenv("CI_RUNNER_PUBLIC_KEY_PATH"), filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud.pub"))
	allowed := os.Getenv("CI_RUNNER_ALLOWED_SSH_CIDRS")
	if allowed == "" {
		return errors.New("CI_RUNNER_ALLOWED_SSH_CIDRS is required")
	}
	key, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return err
	}
	region := firstNonEmpty(os.Getenv("CI_RUNNER_REGION"), "us-sea")
	image := firstNonEmpty(os.Getenv("CI_RUNNER_IMAGE"), "linode/ubuntu24.04")
	seen := map[string]bool{}
	for _, spec := range runner.Specs() {
		if seen[spec.HostLabel] {
			continue
		}
		seen[spec.HostLabel] = true
		rootPass, err := randomPassword()
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"label":           spec.HostLabel,
			"region":          region,
			"type":            spec.Type,
			"image":           image,
			"root_pass":       rootPass,
			"authorized_keys": []string{strings.TrimSpace(string(key))},
			"tags":            []string{"rtk-cloud-ci", "github-runner"},
		})
		created, err := curlLinode("POST", "/linode/instances", string(payload))
		if err != nil {
			return err
		}
		var vm struct {
			ID   int      `json:"id"`
			IPv4 []string `json:"ipv4"`
		}
		if err := json.Unmarshal(created, &vm); err != nil {
			return err
		}
		fwPayload, _ := json.Marshal(map[string]any{
			"label": spec.HostLabel + "-firewall",
			"rules": map[string]any{
				"inbound_policy":  "DROP",
				"outbound_policy": "ACCEPT",
				"inbound": []map[string]any{{
					"label": "ssh", "action": "ACCEPT", "protocol": "TCP", "ports": "22",
					"addresses": map[string]any{"ipv4": strings.Split(allowed, ",")},
				}},
				"outbound": []any{},
			},
		})
		fw, err := curlLinode("POST", "/networking/firewalls", string(fwPayload))
		if err != nil {
			return err
		}
		var firewall struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(fw, &firewall)
		if firewall.ID != 0 && vm.ID != 0 {
			device, _ := json.Marshal(map[string]any{"id": vm.ID, "type": "linode"})
			if _, err := curlLinode("POST", fmt.Sprintf("/networking/firewalls/%d/devices", firewall.ID), string(device)); err != nil {
				return err
			}
		}
		publicIP := ""
		if len(vm.IPv4) > 0 {
			publicIP = vm.IPv4[0]
		}
		fmt.Fprintf(os.Stdout, "%s\tcreated\t%s\n", spec.HostLabel, publicIP)
	}
	return nil
}

func archiveCIRunnerArtifacts(args []string) error {
	fs := flag.NewFlagSet("ci-runners archive-artifacts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", "", "GitHub repo")
	runID := fs.String("run-id", "", "GitHub Actions run id")
	outDir := fs.String("out-dir", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *runID == "" {
		return errors.New("--repo and --run-id are required")
	}
	if *outDir == "" {
		*outDir = filepath.Join(".artifacts", "ci-runners", strings.ReplaceAll(*repo, "/", "-"), *runID)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("gh", "run", "download", *runID, "--repo", *repo, "--dir", *outDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	meta := map[string]any{"repo": *repo, "run_id": *runID, "artifact_dir": *outDir, "archived_at": time.Now().UTC().Format(time.RFC3339)}
	return writeJSON(filepath.Join(*outDir, "archive-metadata.json"), meta)
}

func runCIRunnerSession(args []string) error {
	fs := flag.NewFlagSet("ci-runners run-session", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runIDs := map[string]*string{
		"hkt999rtk/rtk_account_manager": fs.String("account-run-id", "", "Account Manager run id"),
		"hkt999rtk/rtk_cloud_admin":     fs.String("admin-run-id", "", "Cloud Admin run id"),
		"hkt999rtk/rtk_cloud_frontend":  fs.String("frontend-run-id", "", "Frontend run id"),
		"hkt999rtk/rtk_cloud_client":    fs.String("client-run-id", "", "Client run id"),
		"hkt999rtk/rtk_cloud_logger":    fs.String("logger-run-id", "", "Logger run id"),
	}
	rerun := fs.Bool("rerun", true, "rerun selected workflows")
	shutdownPolicy := fs.String("shutdown-policy", "always", "always, on-success, or never")
	smokeOnly := fs.Bool("smoke-only", false, "only start/wait/shutdown")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shutdownPolicy != "always" && *shutdownPolicy != "on-success" && *shutdownPolicy != "never" {
		return errors.New("--shutdown-policy must be always, on-success, or never")
	}
	if err := runCIRunnersPower([]string{"start"}); err != nil {
		return err
	}
	shouldShutdown := *shutdownPolicy == "always"
	defer func() {
		if shouldShutdown {
			_ = runCIRunnersPower([]string{"stop"})
		}
	}()
	if err := runCIRunnersWaitOnline(nil); err != nil {
		return err
	}
	if *smokeOnly {
		if *shutdownPolicy == "on-success" {
			shouldShutdown = true
		}
		return nil
	}
	overall := 0
	for repo, idPtr := range runIDs {
		if *idPtr == "" {
			continue
		}
		if *rerun {
			if err := runExternal("gh", "run", "rerun", *idPtr, "--repo", repo); err != nil {
				overall = 1
			}
		}
		if err := runExternal("gh", "run", "watch", *idPtr, "--repo", repo, "--exit-status"); err != nil {
			overall = 1
		}
		if err := archiveCIRunnerArtifacts([]string{"--repo", repo, "--run-id", *idPtr}); err != nil {
			overall = 1
		}
	}
	if *shutdownPolicy == "on-success" && overall == 0 {
		shouldShutdown = true
	}
	if overall != 0 {
		return exitCode(overall)
	}
	return nil
}

func runExternal(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
