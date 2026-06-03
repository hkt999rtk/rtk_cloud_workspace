package main

import (
	"path/filepath"
	"strings"
	"testing"
)

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
	url = git@github.com-work:hkt999rtk/rtk_cloud_contracts_doc.git
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
	url = https://github.com/hkt999rtk/rtk_cloud_contracts_doc.git
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
	checkContractsPolicy(check, root, commits)

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
