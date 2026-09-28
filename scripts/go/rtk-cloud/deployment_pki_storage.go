package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// runDeploymentPKIStoragePlan shows the selected environment's PVC plan. It
// deliberately has no apply path; protected-environment rollout is separate.
func runDeploymentPKIStoragePlan(args []string) error {
	fs := flag.NewFlagSet("deployment pki-storage-plan", flag.ContinueOnError)
	environment := fs.String("environment", "", "dev, staging, or prod")
	workspace := fs.String("workspace", "", "workspace root")
	live := fs.Bool("live", false, "compare with current PVCs without changing them")
	render := fs.Bool("render", false, "print review-only PVC manifest")
	cleanupAudit := fs.Bool("cleanup-audit", false, "read-only dev legacy PVC cleanup gate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" {
		return errors.New("--environment is required and positional arguments are not accepted")
	}
	if *workspace == "" {
		root, err := workspaceRoot()
		if err != nil {
			return err
		}
		*workspace = root
	}
	script := filepath.Join(*workspace, "scripts", "pki-consumer-storage-plan.py")
	arguments := []string{script, "--workspace", *workspace, "--environment", *environment}
	if *live {
		arguments = append(arguments, "--live")
	}
	if *render {
		arguments = append(arguments, "--render")
	}
	if *cleanupAudit {
		arguments = append(arguments, "--cleanup-audit")
	}
	cmd := exec.Command("python3", arguments...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("PKI storage plan: %w", err)
	}
	return nil
}
