package main

import (
	"path/filepath"
	"strings"
)

func selectChangedServiceRepos(changedFiles []string) []string {
	selected := map[string]bool{}
	selectAll := false
	for _, changed := range changedFiles {
		changed = filepath.ToSlash(strings.TrimSpace(changed))
		if changed == "" {
			continue
		}
		switch {
		case changed == ".gitmodules",
			changed == "go.work",
			changed == "go.work.sum",
			strings.HasPrefix(changed, "scripts/go/rtk-cloud/"),
			changed == "repos/rtk_cloud_contracts_doc":
			selectAll = true
		}
		for _, repo := range managedServiceRepos {
			prefix := "repos/" + repo
			if changed == prefix || strings.HasPrefix(changed, prefix+"/") {
				selected[repo] = true
			}
		}
	}
	if selectAll {
		for _, repo := range managedServiceRepos {
			selected[repo] = true
		}
	}
	repos := make([]string, 0, len(selected))
	for _, repo := range managedServiceRepos {
		if selected[repo] {
			repos = append(repos, repo)
		}
	}
	return repos
}
