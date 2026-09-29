package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// runSchema keeps the database documentation commands under the workspace CLI.
func runSchema(args []string) error {
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	command := exec.Command("python3", append([]string{
		filepath.Join(workspace, "scripts/database_schema_catalog.py"),
	}, args...)...)
	command.Dir = workspace
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if exited, ok := err.(*exec.ExitError); ok {
			return exitCode(exited.ExitCode())
		}
		return fmt.Errorf("database schema command failed: %w", err)
	}
	return nil
}
