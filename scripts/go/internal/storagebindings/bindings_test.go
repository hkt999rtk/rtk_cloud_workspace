// These checks verify repository bindings against the shared policy implementation.
// They do not inspect credentials or access Object Storage.
package storagebindings

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/internal/storagepolicy"
)

const policyURL = "https://github.com/hkt999rtk/rtk_cloud_workspace/blob/main/docs/object-storage-policy.md"

type workflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name string `yaml:"name"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func workspace(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate workspace source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "../../../.."))
}

func read(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workspace(t), filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s (initialize pinned submodules first): %v", relative, err)
	}
	return string(data)
}

func load(t *testing.T, repo, file string) workflow {
	t.Helper()
	var result workflow
	if err := yaml.Unmarshal([]byte(read(t, "repos/"+repo+"/.github/workflows/"+file)), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assignment(t *testing.T, script, variable, repo string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(variable) + `="([^"]+)"$`)
	match := pattern.FindStringSubmatch(script)
	if len(match) != 2 {
		t.Fatalf("missing unambiguous %s assignment", variable)
	}
	return strings.NewReplacer("${GITHUB_REPOSITORY#*/}", repo, "${GITHUB_RUN_ID}", "123", "${GITHUB_RUN_ATTEMPT}", "2").Replace(match[1])
}

func TestCIPublishBindings(t *testing.T) {
	cases := map[string]map[string]string{
		"rtk_cloud_client": {
			"Upload Linux CI artifacts to Linode Object Storage":            "linux",
			"Upload macOS CI artifacts to Linode Object Storage":            "macos",
			"Upload Android emulator CI artifacts to Linode Object Storage": "android-emulator",
		},
		"rtk_video_cloud": {"Upload CI artifacts to Linode Object Storage": "linux"},
	}
	for repo, expectedSteps := range cases {
		t.Run(repo, func(t *testing.T) {
			found := map[string]bool{}
			for _, job := range load(t, repo, "ci.yml").Jobs {
				for _, step := range job.Steps {
					platform, ok := expectedSteps[step.Name]
					if !ok {
						if strings.Contains(step.Run, "--key") && (strings.Contains(step.Run, "linode-object-storage") || strings.Contains(step.Run, "./cmd/s3put")) {
							t.Errorf("unregistered CI Object Storage producer %q", step.Name)
						}
						continue
					}
					want, err := storagepolicy.CIKeyPrefix(repo, "123", "2", platform)
					if err != nil {
						t.Fatal(err)
					}
					if got := assignment(t, step.Run, "prefix", repo); got != want {
						t.Errorf("%s prefix = %q, want %q", step.Name, got, want)
					}
					for _, binding := range []string{`export OBJECT_STORAGE_PREFIX="$prefix"`, `os.environ['OBJECT_STORAGE_PREFIX']`, `--key "$prefix/`} {
						// The client uses braces around prefix in its upload commands.
						if !strings.Contains(strings.ReplaceAll(step.Run, "${prefix}", "$prefix"), binding) {
							t.Errorf("%s does not share prefix with manifest/upload: %s", step.Name, binding)
						}
					}
					found[step.Name] = true
				}
			}
			if len(found) != len(expectedSteps) {
				t.Fatalf("checked %d of %d CI producers", len(found), len(expectedSteps))
			}
		})
	}
}

func TestSDKTemporaryBindings(t *testing.T) {
	prefix, err := storagepolicy.TmpKeyPrefix("rtk-cloud-client-sdk", "123", "2")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for jobName, job := range load(t, "rtk_cloud_client", "sdk-portal-release.yml").Jobs {
		for _, step := range job.Steps {
			switch step.Name {
			case "Upload run-scoped staging objects":
				platform := strings.TrimSuffix(jobName, "-packages")
				if got := assignment(t, step.Run, "key", "rtk_cloud_client"); got != prefix+"/"+platform+".tar.gz" {
					t.Errorf("%s temporary key = %q", jobName, got)
				}
				found[platform] = true
			case "Download and assemble platform outputs":
				if got := assignment(t, step.Run, "prefix", "rtk_cloud_client"); got != prefix {
					t.Errorf("assembler prefix = %q, want %q", got, prefix)
				}
				for _, platform := range []string{"linux", "macos"} {
					if !strings.Contains(step.Run, `download --key "$prefix/`+platform+`.tar.gz"`) {
						t.Errorf("assembler does not read %s temporary object", platform)
					}
				}
				found["assembler"] = true
			}
		}
	}
	if len(found) != 3 || !found["linux"] || !found["macos"] || !found["assembler"] {
		t.Fatalf("incomplete temporary bindings: %v", found)
	}
}

func TestStandaloneRepositoryPolicyReferences(t *testing.T) {
	read(t, "docs/object-storage-policy.md")
	for _, repo := range []string{"rtk_cloud_client", "rtk_video_cloud", "rtk_cloud_frontend", "rtk_account_manager", "rtk_cloud_admin", "rtk_cloud_logger"} {
		if !strings.Contains(read(t, "repos/"+repo+"/README.md"), policyURL) {
			t.Errorf("%s README must reference the canonical policy with a standalone link", repo)
		}
	}
}
