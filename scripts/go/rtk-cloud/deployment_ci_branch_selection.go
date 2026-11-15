package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// CI_ROLLOUT_BRANCHES is maintained environment intent, mapping an exact
// owner/repository to its selected Git branch. Absence selects nothing.
type ciBranchSelection map[string]string

var ciRepositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (s ciBranchSelection) matches(repository, branch string) bool {
	return branch != "" && s[repository] == branch
}

func parseCIBranchSelection(raw string) (ciBranchSelection, error) {
	selected := ciBranchSelection{}
	if raw == "" {
		return selected, nil
	}
	bad := func() (ciBranchSelection, error) {
		return nil, errors.New("CI_ROLLOUT_BRANCHES must map distinct repository names to valid explicit Git branches")
	}
	if len(raw) > int(environmentInputMaxBytes) {
		return bad()
	}
	d := json.NewDecoder(strings.NewReader(raw))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return bad()
	}
	for d.More() {
		token, err := d.Token()
		repository, ok := token.(string)
		if err != nil || !ok || !ciRepositoryName.MatchString(repository) {
			return bad()
		}
		parts := strings.Split(repository, "/")
		if parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			return bad()
		}
		if _, exists := selected[repository]; exists {
			return bad()
		}
		var branch string
		if d.Decode(&branch) != nil || !validCIBranchName(branch) {
			return bad()
		}
		selected[repository] = branch
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return bad()
	}
	return selected, nil
}

func validCIBranchName(branch string) bool {
	if branch == "" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") || strings.ContainsAny(branch, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Branch tracking is an independent environment setting; deployment facts and
// per-run observations are kept in the selected environment SecretStore.
func readCIBranchSelection(cfg deploymentConfig) (ciBranchSelection, error) {
	if cfg.Environment == "" || cfg.EnvironmentRoot == "" {
		return nil, errors.New("selected environment configuration is required")
	}
	values, err := readOptionalStrictEnv(filepath.Join(cfg.EnvironmentRoot, "ci-rollout.env"))
	if err != nil {
		return nil, err
	}
	for key := range values {
		if key != "CI_ROLLOUT_BRANCHES" {
			return nil, errors.New("unknown key in environment ci-rollout.env: " + key)
		}
	}
	return parseCIBranchSelection(values["CI_ROLLOUT_BRANCHES"])
}
