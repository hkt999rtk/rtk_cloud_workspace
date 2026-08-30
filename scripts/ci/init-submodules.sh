#!/usr/bin/env bash
set -euo pipefail

: "${CI_RUNNER_GITHUB_WORK_KEY:?CI_RUNNER_GITHUB_WORK_KEY is required}"
install -d -m 0700 "$HOME/.ssh"
printf '%s\n' "$CI_RUNNER_GITHUB_WORK_KEY" > "$HOME/.ssh/id_ed25519_github_work"
chmod 0600 "$HOME/.ssh/id_ed25519_github_work"
ssh-keyscan github.com >> "$HOME/.ssh/known_hosts"
git config --global core.sshCommand "ssh -i $HOME/.ssh/id_ed25519_github_work -o IdentitiesOnly=yes"
git config --global --add url."git@github.com:".insteadOf "git@github-work.com:"
git config --global --add url."git@github.com:".insteadOf "git@github.com-work:"
# Self-hosted workspaces can retain nested-submodule files from an earlier
# checkout. Recreate every submodule worktree so a stale file or symlink cannot
# block the commit pinned by the current workspace checkout.
git submodule deinit --all --force
git submodule sync --recursive
git submodule update --init --recursive
