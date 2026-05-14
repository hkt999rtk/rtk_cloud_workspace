from __future__ import annotations

import subprocess
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class RepositoryInfo:
    name: str
    path: str
    commit_sha: str
    branch_or_detached_state: str
    dirty: bool


def run_git(path: Path, *args: str) -> str:
    try:
        return subprocess.check_output(["git", *args], cwd=path, text=True, stderr=subprocess.DEVNULL).strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return ""


def repository_info(workspace: Path, repo_path: Path) -> RepositoryInfo:
    rel = repo_path.relative_to(workspace).as_posix()
    name = repo_path.name if rel != "." else workspace.name
    commit = run_git(repo_path, "rev-parse", "HEAD") or "unknown"
    branch = run_git(repo_path, "branch", "--show-current")
    if not branch:
        branch = f"detached:{commit[:12]}" if commit != "unknown" else "unknown"
    dirty = bool(run_git(repo_path, "status", "--short"))
    return RepositoryInfo(name=name, path=rel, commit_sha=commit, branch_or_detached_state=branch, dirty=dirty)


def discover_repositories(workspace: Path) -> list[RepositoryInfo]:
    workspace = workspace.resolve()
    repos = [repository_info(workspace, workspace)]
    repos_dir = workspace / "repos"
    if repos_dir.exists():
        for child in sorted(item for item in repos_dir.iterdir() if item.is_dir()):
            if (child / ".git").exists():
                repos.append(repository_info(workspace, child))
    return repos


def repo_for_path(workspace: Path, file_path: Path) -> RepositoryInfo:
    workspace = workspace.resolve()
    file_path = file_path.resolve()
    candidates = discover_repositories(workspace)
    matches = []
    for repo in candidates:
        root = workspace if repo.path == "." else workspace / repo.path
        try:
            file_path.relative_to(root)
            matches.append((len(root.as_posix()), repo))
        except ValueError:
            continue
    return sorted(matches, reverse=True)[0][1] if matches else repository_info(workspace, workspace)


def classify_document(relative_path: Path | str) -> tuple[str, str]:
    path = Path(relative_path).as_posix()
    name = Path(path).name.lower()

    if path.startswith("repos/rtk_cloud_contracts_doc/"):
        return "source", "contracts"
    if "/rtk_cloud_contracts_doc/" in path:
        return "reference-only", "generated"
    if path.startswith("docs/"):
        if "/adr/" in path or name == "documentation-governance.md" or name in {"architecture.md", "readme.md"}:
            return "source", "workspace"
        return "supporting-note", "workspace"
    if path.endswith((".go", ".js", ".jsx", ".mjs", ".ts", ".tsx")):
        return "source", "code"
    if name in {"readme.md", "openapi.yaml", "openapi.yml"}:
        return "source", "service"
    if "/docs/" in path:
        return "source", "service"
    if path.endswith((".yaml", ".yml", ".toml", ".json")):
        return "source", "service"
    return "supporting-note", "service"

