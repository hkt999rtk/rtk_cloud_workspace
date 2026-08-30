package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckContractsPolicyCanonicalLinks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string, map[string]string)
		want   string
	}{
		{name: "aligned canonical links"},
		{name: "missing link", change: func(t *testing.T, root string, commits map[string]string) {
			delete(commits, expectedContractsPaths[1])
		}, want: "contracts link unavailable"},
		{name: "copied directory", change: func(t *testing.T, root string, _ map[string]string) {
			mkdirAll(t, filepath.Join(root, expectedContractsPaths[1]))
		}, want: "registered submodule or canonical symlink"},
		{name: "dangling link", change: func(t *testing.T, root string, _ map[string]string) {
			makeContractsTestLink(t, root, "../../missing")
		}, want: "contracts link cannot be resolved"},
		{name: "different checkout same commit", change: func(t *testing.T, root string, _ map[string]string) {
			mkdirAll(t, filepath.Join(root, "repos/copy"))
			makeContractsTestLink(t, root, "../../copy")
		}, want: "does not resolve to the canonical"},
		{name: "registration cannot hide redirected link", change: func(t *testing.T, root string, _ map[string]string) {
			mkdirAll(t, filepath.Join(root, "repos/copy"))
			makeContractsTestLink(t, root, "../../copy")
			writeFile(t, filepath.Join(root, "repos/rtk_account_manager/.gitmodules"), "[submodule \"contracts\"]\npath = docs/rtk_cloud_contracts_doc\nurl = "+contractsRepoURL+"\n")
		}, want: "does not resolve to the canonical"},
		{name: "missing canonical checkout", change: func(t *testing.T, root string, _ map[string]string) {
			// Keep one consumer resolvable so the canonical-path error is exercised.
			mkdirAll(t, filepath.Join(root, "repos/copy"))
			makeContractsTestLink(t, root, "../../copy")
			if err := os.Remove(filepath.Join(root, expectedContractsPaths[0])); err != nil {
				t.Fatal(err)
			}
		}, want: "canonical contracts checkout unavailable"},
		{name: "missing root registration", change: func(t *testing.T, root string, _ map[string]string) {
			writeFile(t, filepath.Join(root, ".gitmodules"), "")
			makeContractsTestLink(t, root, "../../rtk_cloud_contracts_doc")
		}, want: "contracts submodule path missing: repos/rtk_cloud_contracts_doc"},
		{name: "missing root commit", change: func(t *testing.T, root string, commits map[string]string) {
			delete(commits, expectedContractsPaths[0])
			makeContractsTestLink(t, root, "../../rtk_cloud_contracts_doc")
		}, want: "root contracts commit missing"},
		{name: "consumer commit drift", change: func(t *testing.T, root string, commits map[string]string) {
			commits[expectedContractsPaths[1]] = "other"
			makeContractsTestLink(t, root, "../../rtk_cloud_contracts_doc")
		}, want: "is pinned to other"},
		{name: "unreadable gitmodules", change: func(t *testing.T, root string, _ map[string]string) {
			mkdirAll(t, filepath.Join(root, "repos/rtk_cloud_admin/.gitmodules"))
		}, want: "could not read .gitmodules"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mkdirAll(t, filepath.Join(root, expectedContractsPaths[0]))
			writeFile(t, filepath.Join(root, ".gitmodules"), "[submodule \"contracts\"]\npath = "+expectedContractsPaths[0]+"\nurl = "+contractsRepoURL+"\n")
			commits := map[string]string{expectedContractsPaths[0]: "same"}
			for _, path := range expectedContractsPaths[1:] {
				mkdirAll(t, filepath.Dir(filepath.Join(root, path)))
				commits[path] = "same"
				if path != expectedContractsPaths[1] {
					if err := os.Symlink("../../rtk_cloud_contracts_doc", filepath.Join(root, path)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.change != nil {
				tc.change(t, root, commits)
			} else {
				makeContractsTestLink(t, root, "../../rtk_cloud_contracts_doc")
			}
			check := newCheck()
			stdout, stderr, _ := captureOutput(func() error {
				checkContractsPolicy(check, root, commits)
				return nil
			})
			if tc.want == "" {
				if check.failures != 0 {
					t.Fatalf("canonical links rejected: %s%s", stdout, stderr)
				}
			} else if check.failures == 0 || !strings.Contains(stdout+stderr, tc.want) {
				t.Fatalf("expected failure %q, got %s%s", tc.want, stdout, stderr)
			}
		})
	}
}

func makeContractsTestLink(t *testing.T, root, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(root, expectedContractsPaths[1])); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalContractsURLs(t *testing.T) {
	for _, url := range []string{contractsRepoURL, contractsRepoLegacySSHURL, contractsRepoHTTPSURL} {
		if !isCanonicalContractsURL(url) {
			t.Errorf("canonical URL rejected: %s", url)
		}
	}
	for _, url := range []string{
		"https://github.com/other/rtk_cloud_contracts_doc.git",
		"https://example.com/hkt999rtk/rtk_cloud_contracts_doc.git",
		"https://x-access-token:secret@github.com/hkt999rtk/rtk_cloud_contracts_doc.git",
	} {
		if isCanonicalContractsURL(url) {
			t.Error("noncanonical URL accepted")
		}
	}
}

func TestCheckContractsPolicyAcceptsStandardPathsURLsAndAlignedCommits(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitmodules"), `
[submodule "repos/rtk_cloud_contracts_doc"]
	path = repos/rtk_cloud_contracts_doc
	url = git@github.com-work:hkt999rtk/rtk_cloud_contracts_doc.git
[submodule "repos/rtk_video_cloud"]
	path = repos/rtk_video_cloud
	url = git@github.com-work:hkt999rtk/rtk_video_cloud.git
`)
	mkdirAll(t, filepath.Join(root, "repos", "rtk_video_cloud"))
	writeFile(t, filepath.Join(root, "repos", "rtk_video_cloud", ".gitmodules"), `
[submodule "rtk_cloud_contracts_doc"]
	path = docs/rtk_cloud_contracts_doc
	url = https://github.com/hkt999rtk/rtk_cloud_contracts_doc.git
`)
	for _, repo := range []string{"rtk_cloud_client", "rtk_account_manager", "rtk_cloud_admin"} {
		mkdirAll(t, filepath.Join(root, "repos", repo))
		writeFile(t, filepath.Join(root, "repos", repo, ".gitmodules"), `
[submodule "rtk_cloud_contracts_doc"]
	path = docs/rtk_cloud_contracts_doc
	url = git@github.com-work:hkt999rtk/rtk_cloud_contracts_doc.git
`)
	}
	commits := map[string]string{
		"repos/rtk_cloud_contracts_doc":                          "abc123",
		"repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc":     "abc123",
		"repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc":    "abc123",
		"repos/rtk_account_manager/docs/rtk_cloud_contracts_doc": "abc123",
		"repos/rtk_cloud_admin/docs/rtk_cloud_contracts_doc":     "abc123",
	}

	check := newCheck()
	checkContractsPolicy(check, root, commits)

	if check.failures != 0 {
		t.Fatalf("checkContractsPolicy failures=%d", check.failures)
	}
}

func TestCheckContractsPolicyRejectsLegacyPathsURLsAndDrift(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitmodules"), `
[submodule "repos/rtk_cloud_contracts_doc"]
	path = repos/rtk_cloud_contracts_doc
	url = https://x-access-token:secret@github.com/hkt999rtk/rtk_cloud_contracts_doc.git
`)
	mkdirAll(t, filepath.Join(root, "repos", "rtk_account_manager"))
	writeFile(t, filepath.Join(root, "repos", "rtk_account_manager", ".gitmodules"), `
[submodule "contracts"]
	path = contracts
	url = https://github.com/hkt999rtk/rtk_cloud_contracts_doc.git
`)
	commits := map[string]string{
		"repos/rtk_cloud_contracts_doc":                       "root",
		"repos/rtk_account_manager/contracts":                 "old",
		"repos/rtk_cloud_admin/rtk_cloud_contracts_doc":       "root",
		"repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc": "root",
		"repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc":  "root",
	}

	check := newCheck()
	stdout, stderr, _ := captureOutput(func() error {
		checkContractsPolicy(check, root, commits)
		return nil
	})
	if strings.Contains(stdout+stderr, "secret") {
		t.Fatal("policy report exposed credentials from a rejected URL")
	}

	if check.failures == 0 {
		t.Fatal("checkContractsPolicy accepted legacy contracts policy")
	}
}

func TestRunCommandIncludesContractsCheck(t *testing.T) {
	if _, ok := commands["contracts-check"]; !ok {
		t.Fatal("contracts-check command is not registered")
	}
}

func TestRenderContractsPolicyReportDocumentsStandardPath(t *testing.T) {
	report := renderContractsPolicyReport("abc123", []contractsPolicyFinding{
		{Status: "pass", Detail: "repos/rtk_video_cloud/docs/rtk_cloud_contracts_doc aligned"},
	})
	for _, want := range []string{"contracts_root_commit=abc123", "docs/rtk_cloud_contracts_doc"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}
