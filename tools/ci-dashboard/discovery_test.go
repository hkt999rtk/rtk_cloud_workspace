package main

import "testing"

func TestParseGitHubRemote(t *testing.T) {
	tests := []struct {
		remote, owner, repo string
		ok                  bool
	}{
		{"git@github-work.com:hkt999rtk/rtk_cloud_workspace.git", "hkt999rtk", "rtk_cloud_workspace", true},
		{"https://github.com/hkt999rtk/rtk_cloud_admin.git", "hkt999rtk", "rtk_cloud_admin", true},
		{"ssh://git@github.com/hkt999rtk/rtk_cloud_client", "hkt999rtk", "rtk_cloud_client", true},
		{"git@gitlab.com:hkt999rtk/nope.git", "", "", false},
	}
	for _, test := range tests {
		owner, repo, ok := parseGitHubRemote(test.remote)
		if owner != test.owner || repo != test.repo || ok != test.ok {
			t.Errorf("parseGitHubRemote(%q) = %q, %q, %v", test.remote, owner, repo, ok)
		}
	}
}
