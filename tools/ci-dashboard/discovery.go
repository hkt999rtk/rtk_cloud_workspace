package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var githubRemotePattern = regexp.MustCompile(`(?i)(?:github(?:-[^./:]+)?\.com)[:/]([^/]+)/([^/]+?)(?:\.git)?$`)

func discoverRepositories(workspace, allowedOwner string) ([]Repository, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	rootURL, err := gitRemote(workspace)
	if err != nil {
		return nil, fmt.Errorf("workspace origin: %w", err)
	}
	owner, name, ok := parseGitHubRemote(rootURL)
	if !ok || !strings.EqualFold(owner, allowedOwner) {
		return nil, fmt.Errorf("workspace origin %q is not a %s GitHub repository", rootURL, allowedOwner)
	}
	repos := []Repository{{Owner: owner, Name: name, Path: workspace}}

	f, err := os.Open(filepath.Join(workspace, ".gitmodules"))
	if err != nil {
		return nil, fmt.Errorf("open .gitmodules: %w", err)
	}
	defer f.Close()
	var path, url string
	flush := func() {
		if path == "" || url == "" {
			path, url = "", ""
			return
		}
		o, n, found := parseGitHubRemote(url)
		if found && strings.EqualFold(o, allowedOwner) {
			repos = append(repos, Repository{Owner: o, Name: n, Path: filepath.Join(workspace, path), IsSubmodule: true})
		}
		path, url = "", ""
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[submodule ") {
			flush()
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "path":
			path = strings.TrimSpace(value)
		case "url":
			url = strings.TrimSpace(value)
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return repos, nil
}

func gitRemote(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func parseGitHubRemote(remote string) (owner, repo string, ok bool) {
	matches := githubRemotePattern.FindStringSubmatch(strings.TrimSpace(remote))
	if len(matches) != 3 {
		return "", "", false
	}
	return matches[1], strings.TrimSuffix(matches[2], ".git"), true
}
