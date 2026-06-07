package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const contractsRepoURL = "git@github.com-work:hkt999rtk/rtk_cloud_contracts_doc.git"
const contractsRepoHTTPSURL = "https://github.com/hkt999rtk/rtk_cloud_contracts_doc.git"

var expectedContractsPaths = []string{
	"repos/rtk_cloud_contracts_doc",
	"repos/rtk_account_manager/docs/rtk_cloud_contracts_doc",
	"repos/rtk_cloud_admin/docs/rtk_cloud_contracts_doc",
	"repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc",
	"repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc",
}

type gitmoduleEntry struct {
	File string
	Name string
	Path string
	URL  string
}

type contractsPolicyFinding struct {
	Status string
	Detail string
}

func runContractsCheck(args []string) error {
	fs := flag.NewFlagSet("contracts-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	check := newCheck()
	commits := collectContractsCommits(workspace)
	checkContractsPolicy(check, workspace, commits)
	if check.failures > 0 {
		return exitCode(1)
	}
	return nil
}

func checkContractsPolicy(check *checkState, workspace string, commits map[string]string) {
	rootCommit := commits["repos/rtk_cloud_contracts_doc"]
	fmt.Fprint(os.Stdout, renderContractsPolicyReport(rootCommit, nil))

	entries, err := collectGitmoduleEntries(workspace)
	if err != nil {
		check.fail("could not read .gitmodules files: " + err.Error())
		return
	}
	byPath := map[string]gitmoduleEntry{}
	for _, entry := range entries {
		if !isCanonicalContractsURL(entry.URL) && strings.Contains(entry.URL, "rtk_cloud_contracts_doc") {
			check.fail(fmt.Sprintf("%s uses non-standard contracts URL %s", entry.File, entry.URL))
		}
		if !isCanonicalContractsURL(entry.URL) {
			continue
		}
		fullPath := entry.Path
		if entry.File != ".gitmodules" {
			fullPath = filepath.ToSlash(filepath.Join(filepath.Dir(entry.File), entry.Path))
		}
		byPath[fullPath] = entry
	}
	for _, path := range expectedContractsPaths {
		if _, ok := byPath[path]; ok {
			check.pass("contracts submodule path registered: " + path)
		} else {
			check.fail("contracts submodule path missing: " + path)
		}
	}
	for path := range byPath {
		if !isExpectedContractsPath(path) {
			check.fail("non-standard contracts submodule path: " + path)
		}
	}
	if rootCommit == "" {
		check.fail("root contracts commit missing: repos/rtk_cloud_contracts_doc")
		return
	}
	check.pass("root contracts commit: " + rootCommit)
	for _, path := range expectedContractsPaths[1:] {
		commit := commits[path]
		if commit == "" {
			check.fail("contracts commit missing: " + path)
			continue
		}
		if commit != rootCommit {
			check.fail(fmt.Sprintf("%s is pinned to %s, root contracts is %s", path, commit, rootCommit))
			continue
		}
		check.pass("contracts commit aligned: " + path)
	}
}

func renderContractsPolicyReport(rootCommit string, findings []contractsPolicyFinding) string {
	var b strings.Builder
	b.WriteString("== contracts submodule policy ==\n")
	if rootCommit != "" {
		b.WriteString("contracts_root_commit=" + rootCommit + "\n")
	}
	b.WriteString("contracts_standard_path=docs/rtk_cloud_contracts_doc\n")
	b.WriteString("contracts_standard_url=" + contractsRepoURL + "\n")
	b.WriteString("contracts_standard_https_url=" + contractsRepoHTTPSURL + "\n")
	for _, finding := range findings {
		b.WriteString(finding.Status + ": " + finding.Detail + "\n")
	}
	return b.String()
}

func isCanonicalContractsURL(url string) bool {
	return url == contractsRepoURL || url == contractsRepoHTTPSURL
}

func isExpectedContractsPath(path string) bool {
	for _, expected := range expectedContractsPaths {
		if path == expected {
			return true
		}
	}
	return false
}

func collectContractsCommits(workspace string) map[string]string {
	out := map[string]string{}
	for _, path := range expectedContractsPaths {
		abs := filepath.Join(workspace, filepath.FromSlash(path))
		if !exists(abs) {
			continue
		}
		commit, err := gitOutput(abs, "rev-parse", "HEAD")
		if err == nil {
			out[path] = strings.TrimSpace(commit)
		}
	}
	return out
}

func collectGitmoduleEntries(workspace string) ([]gitmoduleEntry, error) {
	files := []string{".gitmodules"}
	for _, repo := range []string{
		"repos/rtk_account_manager",
		"repos/rtk_cloud_admin",
		"repos/rtk_cloud_client",
		"repos/rtk_video_cloud",
	} {
		if exists(filepath.Join(workspace, repo, ".gitmodules")) {
			files = append(files, filepath.ToSlash(filepath.Join(repo, ".gitmodules")))
		}
	}
	entries := []gitmoduleEntry{}
	for _, file := range files {
		parsed, err := parseGitmodulesFile(filepath.Join(workspace, filepath.FromSlash(file)), file)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		entries = append(entries, parsed...)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].File == entries[j].File {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].File < entries[j].File
	})
	return entries, nil
}

func parseGitmodulesFile(path, label string) ([]gitmoduleEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries := []gitmoduleEntry{}
	current := gitmoduleEntry{File: label}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[submodule ") {
			if current.Name != "" {
				entries = append(entries, current)
			}
			current = gitmoduleEntry{File: label, Name: strings.Trim(line, "[]")}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || current.Name == "" {
			continue
		}
		switch strings.TrimSpace(key) {
		case "path":
			current.Path = filepath.ToSlash(strings.TrimSpace(value))
		case "url":
			current.URL = strings.TrimSpace(value)
		}
	}
	if current.Name != "" {
		entries = append(entries, current)
	}
	return entries, nil
}
