package rag

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runGit(path string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = path
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

func repositoryInfo(workspace, repoPath string) RepositoryInfo {
	rel, err := filepath.Rel(workspace, repoPath)
	if err != nil || rel == "" {
		rel = "."
	}
	rel = filepath.ToSlash(rel)
	name := filepath.Base(repoPath)
	if rel == "." {
		name = filepath.Base(workspace)
	}
	commit := runGit(repoPath, "rev-parse", "HEAD")
	if commit == "" {
		commit = "unknown"
	}
	branch := runGit(repoPath, "branch", "--show-current")
	if branch == "" {
		if commit != "unknown" {
			branch = "detached:" + short(commit, 12)
		} else {
			branch = "unknown"
		}
	}
	return RepositoryInfo{
		Name:                  name,
		Path:                  rel,
		CommitSHA:             commit,
		BranchOrDetachedState: branch,
		Dirty:                 runGit(repoPath, "status", "--short") != "",
	}
}

func DiscoverRepositories(workspace string) []RepositoryInfo {
	workspace = mustAbs(workspace)
	repos := []RepositoryInfo{repositoryInfo(workspace, workspace)}
	reposDir := filepath.Join(workspace, "repos")
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		return repos
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(reposDir, entry.Name())
		if _, err := os.Stat(filepath.Join(child, ".git")); err == nil {
			repos = append(repos, repositoryInfo(workspace, child))
		}
	}
	return repos
}

func repoForPath(workspace, filePath string, repos []RepositoryInfo) RepositoryInfo {
	workspace = mustAbs(workspace)
	filePath = mustAbs(filePath)
	if len(repos) == 0 {
		repos = DiscoverRepositories(workspace)
	}
	best := repos[0]
	bestLen := -1
	for _, repo := range repos {
		root := workspace
		if repo.Path != "." {
			root = filepath.Join(workspace, filepath.FromSlash(repo.Path))
		}
		if rel, err := filepath.Rel(root, filePath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if len(root) > bestLen {
				best = repo
				bestLen = len(root)
			}
		}
	}
	return best
}

func ClassifyDocument(relativePath string) (string, string) {
	path := filepath.ToSlash(relativePath)
	name := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(path, "repos/rtk_cloud_contracts_doc/") {
		return "source", "contracts"
	}
	if strings.Contains(path, "/rtk_cloud_contracts_doc/") {
		return "reference-only", "generated"
	}
	if strings.HasPrefix(path, "docs/") {
		if strings.Contains(path, "/adr/") || name == "documentation-governance.md" || name == "architecture.md" || name == "readme.md" {
			return "source", "workspace"
		}
		return "supporting-note", "workspace"
	}
	if hasAnySuffix(path, ".go", ".js", ".jsx", ".mjs", ".ts", ".tsx") {
		return "source", "code"
	}
	if name == "readme.md" || name == "openapi.yaml" || name == "openapi.yml" {
		return "source", "service"
	}
	if strings.Contains(path, "/docs/") {
		return "source", "service"
	}
	if hasAnySuffix(path, ".yaml", ".yml", ".toml", ".json") {
		return "source", "service"
	}
	return "supporting-note", "service"
}
