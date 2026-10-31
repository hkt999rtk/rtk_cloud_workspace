package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A serial runner, an omitted package, duplicate execution, or reused shard
// environment makes this real child-process fixture fail.
func TestWorkspaceCoverageShardsRunRealGoAndMergeEvidence(t *testing.T) {
	workspace := t.TempDir()
	moduleDir := filepath.Join(workspace, "scripts", "go")
	writeCoverageShardFixture(t, moduleDir)
	t.Setenv("GOWORK", "off")
	t.Setenv("RTK_COVERAGE_SERIAL", "")
	barrier := t.TempDir()
	t.Setenv("COVERAGE_FIXTURE_BARRIER", barrier)
	for _, profile := range []string{"unit", "pr"} {
		t.Run(profile, func(t *testing.T) {
			outDir := t.TempDir()
			result := coverageCaseResult{LogPath: "coverage.log"}
			module := coverageModule{Name: "workspace-tooling", Path: "scripts/go", Packages: []string{"./..."}, MinimumStatementPercent: 70, TargetStatementPercent: 80, CoverPackages: []string{"./..."}}
			if err := runGoCoverageModuleProfile(workspace, outDir, filepath.Join(outDir, result.LogPath), coverageConfig{}, module, "", "HEAD", profile, &result); err != nil {
				t.Fatal(err)
			}
			shardEvents, err := filepath.Glob(filepath.Join(outDir, "modules", "workspace-tooling", "shards", "*", "test-events.json"))
			if err != nil || len(shardEvents) != 3 {
				t.Fatalf("shard event streams = %v, error %v; want three real child processes", shardEvents, err)
			}
			if result.Metrics.StatementPercent == nil || *result.Metrics.StatementPercent != 100 {
				t.Fatalf("merged coverage = %#v, want 100%%", result.Metrics)
			}
			units, err := parseGoTestEvents(filepath.Join(outDir, result.TestEventsPath), moduleDir, module)
			if err != nil || len(units) != 5 {
				t.Fatalf("merged inventory = %#v, %v; want all five tests including new-package test", units, err)
			}
			profile := readTestFile(t, filepath.Join(outDir, result.ProfilePath))
			seen := map[string]bool{}
			for _, line := range strings.Split(strings.TrimSpace(profile), "\n")[1:] {
				key := strings.Fields(line)[0]
				if seen[key] {
					t.Fatalf("duplicate coverage block %s", key)
				}
				seen[key] = true
			}
			privateRoots := map[string]bool{}
			for _, path := range shardEvents {
				dir := filepath.Dir(path)
				log := readTestFile(t, filepath.Join(dir, "coverage.log"))
				var roots []string
				for _, line := range strings.Split(log, "\n") {
					if strings.HasPrefix(line, "# TMPDIR=") {
						roots = strings.Fields(line)[1:]
					}
				}
				if len(roots) != 2 {
					t.Fatalf("missing isolated roots: %s", log)
				}
				for _, root := range roots {
					_, value, _ := strings.Cut(root, "=")
					if privateRoots[value] || !filepath.IsAbs(value) || strings.HasPrefix(value, outDir) {
						t.Fatalf("shard reused or misplaced scratch root %s", value)
					}
					privateRoots[value] = true
					if exists(value) {
						t.Fatalf("shard scratch survived join: %s", value)
					}
				}

			}
			timingEvidence := false
			for _, artifact := range result.Evidence {
				if artifact.Kind == "test-timings" && artifact.SHA256 != "" {
					timingEvidence = true
				}
			}
			if !timingEvidence {
				t.Fatal("timing evidence missing from compatible result")
			}
			// Serial comparison uses the original command and gate consumer. Compare
			// statement totals as well as percentages to catch denominator duplication.
			t.Setenv("RTK_COVERAGE_SERIAL", "1")
			t.Setenv("COVERAGE_FIXTURE_BARRIER", "")
			serialOut := t.TempDir()
			serial := coverageCaseResult{LogPath: "coverage.log"}
			if err := runGoCoverageModuleProfile(workspace, serialOut, filepath.Join(serialOut, serial.LogPath), coverageConfig{}, module, "", "HEAD", profile, &serial); err != nil {
				t.Fatal(err)
			}
			_, covered, total, err := parseGoCoverageProfile(filepath.Join(outDir, result.ProfilePath), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, serialCovered, serialTotal, err := parseGoCoverageProfile(filepath.Join(serialOut, serial.ProfilePath), nil)
			if err != nil {
				t.Fatal(err)
			}
			if covered != serialCovered || total != serialTotal {
				t.Fatalf("sharded coverage %d/%d differs from serial %d/%d", covered, total, serialCovered, serialTotal)
			}
			serialUnits, err := parseGoTestEvents(filepath.Join(serialOut, serial.TestEventsPath), moduleDir, module)
			if err != nil {
				t.Fatal(err)
			}
			if len(serialUnits) != len(units) {
				t.Fatalf("serial inventory %d != sharded %d", len(serialUnits), len(units))
			}
			for i, unit := range units {
				if unit.CanonicalKey != serialUnits[i].CanonicalKey || unit.Status != serialUnits[i].Status {
					t.Fatalf("serial/sharded inventory differs at %s", unit.CanonicalKey)
				}
			}
		})
	}
}

func writeCoverageShardFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"go.mod":               "module rtk-cloud-workspace/scripts/go\n\ngo 1.25.0\n",
		"rtk-cloud/fixture.go": "package fixture\nfunc first() int { return 1 }; func second() int { return 2 }; func third() int { return 3 }\n",
		"rtk-cloud/fixture_test.go": `package fixture
import ("testing"; "os"; "time"; "path/filepath"; "fmt"; "os/exec"; "syscall")
func roots(t *testing.T) {
 t.Log(os.Getenv("TMPDIR"), os.Getenv("XDG_CONFIG_HOME"))
 if path:=os.Getenv("COVERAGE_FIXTURE_BLOCK"); path!="" && t.Name()=="TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints" {
  child:=exec.Command("sh","-c","echo $$ > \"$1\"; sleep 60","sh",path+".nested");child.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
  if err:=child.Start();err!=nil{t.Fatal(err)}
  t.Cleanup(func(){syscall.Kill(-child.Process.Pid,syscall.SIGKILL);child.Wait()})
  if err:=os.WriteFile(path,[]byte(fmt.Sprint(os.Getpid())),0600);err!=nil{t.Fatal(err)};time.Sleep(time.Minute)
 }
 if dir:=os.Getenv("COVERAGE_FIXTURE_BARRIER"); dir!="" {
  if err:=os.WriteFile(filepath.Join(dir,t.Name()),[]byte("ready"),0600); err!=nil { t.Fatal(err) }
  deadline:=time.Now().Add(15*time.Second)
  for { entries,err:=os.ReadDir(dir); if err!=nil { t.Fatal(err) }; if len(entries)==3 { break }; if time.Now().After(deadline) { t.Fatal("coverage shards did not execute concurrently") }; time.Sleep(10*time.Millisecond) }
 }
}
func ` + `TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints` + `(t *testing.T) { roots(t); if first()!=1 { t.Fatal("first") } }
func ` + `TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint` + `(t *testing.T) { roots(t); if second()!=2 { t.Fatal("second") } }
func ` + `TestNewTestAutomaticallyRuns` + `(t *testing.T) { roots(t); if third()!=3 { t.Fatal("third") } }
func ` + `TestHistoricalCoreSourceRetainsTwoOriginalServerRequestIDsNewCase` + `(t *testing.T) { if third()!=3 { t.Fatal("prefix test") } }
`,
		"newpackage/fixture.go":      "package extra\nfunc Answer() int { return 42 }\n",
		"newpackage/fixture_test.go": "package extra\nimport \"testing\"\nfunc TestNewPackageAutomaticallyRuns(t *testing.T) { if Answer()!=42 { t.Fatal(\"answer\") } }\n",
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMergeGoCoverageShardsUnionsBlocksAndRejectsCorruption(t *testing.T) {
	for _, tc := range []struct {
		name, a, b, want string
		bad              bool
	}{
		{"set", "mode: set\nx/a.go:1.1,1.2 2 0\nx/b.go:2.1,2.2 1 1\n", "mode: set\nx/a.go:1.1,1.2 2 1\nx/b.go:2.1,2.2 1 0\n", "mode: set\nx/a.go:1.1,1.2 2 1\nx/b.go:2.1,2.2 1 1\n", false},
		{"atomic", "mode: atomic\nx/a.go:1.1,1.2 2 2\n", "mode: atomic\nx/a.go:1.1,1.2 2 3\n", "mode: atomic\nx/a.go:1.1,1.2 2 5\n", false},
		{"count", "mode: count\nx/a.go:1.1,1.2 2 2\n", "mode: count\nx/a.go:1.1,1.2 2 3\n", "mode: count\nx/a.go:1.1,1.2 2 5\n", false},
		{"mode mismatch", "mode: set\nx/a.go:1.1,1.2 2 1\n", "mode: count\nx/a.go:1.1,1.2 2 1\n", "", true},
		{"statement mismatch", "mode: set\nx/a.go:1.1,1.2 2 1\n", "mode: set\nx/a.go:1.1,1.2 3 1\n", "", true},
		{"malformed", "mode: set\nx/a.go:1.1,1.2 2 1\n", "mode: set\nbroken\n", "", true},
		{"missing file", "mode: set\nx/a.go:1.1,1.2 2 1\n", "mode: set\nx/a.go:1.1,1.2 2 1\n", "", true},
		{"empty", "mode: set\nx/a.go:1.1,1.2 2 1\n", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			paths := []string{filepath.Join(root, "a"), filepath.Join(root, "b")}
			os.WriteFile(paths[0], []byte(tc.a), 0600)
			os.WriteFile(paths[1], []byte(tc.b), 0600)
			if tc.name == "missing file" {
				if err := os.Remove(paths[1]); err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(root, "merged")
			err := mergeGoCoverageShardProfiles(paths, output)
			if tc.bad {
				if err == nil {
					t.Fatal("accepted corrupt profile")
				}
				if exists(output) {
					t.Fatal("published corrupt merge")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, output); got != tc.want {
				t.Fatalf("merge=%q want %q", got, tc.want)
			}
		})
	}
}

func TestGoCoverageShardsCancellationDoesNotPublishStaleEvidence(t *testing.T) {
	root := t.TempDir()
	moduleDir := filepath.Join(root, "module")
	writeCoverageShardFixture(t, moduleDir)
	profile := filepath.Join(root, "coverage.out")
	events := filepath.Join(root, "events.json")
	log := filepath.Join(root, "coverage.log")
	os.WriteFile(profile, []byte("stale"), 0600)
	os.WriteFile(events, []byte("stale"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runGoCoverageShards(ctx, moduleDir, log, events, profile, map[string]string{"GOWORK": "off"}, []string{"test", "-json", "-count=1", "-timeout=1m"})
	if err == nil {
		t.Fatal("canceled shards passed")
	}
	for _, path := range []string{profile, events} {
		if exists(path) {
			t.Fatalf("stale or incomplete evidence published at %s", path)
		}
	}
}

func TestMergeGoCoverageShardEventsRejectsMissingAndDuplicateLongTests(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tests []string
		bad   bool
	}{
		{"complete", []string{"TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints", "TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint", "TestNew"}, false},
		{"missing long pass", []string{"TestWrong", "TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint", "TestNew"}, true},
		{"missing stream", []string{"TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints", "TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint"}, true},
		{"malformed events", []string{"TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints", "TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint", "TestNew"}, true},
		{"incomplete test", []string{"TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints", "TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint", "TestNew"}, true},
		{"duplicate", []string{"TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints", "TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint", "TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			var paths []string
			for i, test := range tc.tests {
				path := filepath.Join(root, fmt.Sprint(i))
				paths = append(paths, path)
				os.WriteFile(path, []byte(fmt.Sprintf("{\"Action\":\"run\",\"Package\":\"x/rtk-cloud\",\"Test\":%q}\n{\"Action\":\"pass\",\"Package\":\"x/rtk-cloud\",\"Test\":%q}\n{\"Action\":\"pass\",\"Package\":\"x/rtk-cloud\"}\n", test, test)), 0600)
			}
			if tc.name == "malformed events" {
				if err := os.WriteFile(paths[2], []byte("broken\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "incomplete test" {
				if err := os.WriteFile(paths[2], []byte("{\"Action\":\"run\",\"Package\":\"x/extra\",\"Test\":\"TestNew\"}\n{\"Action\":\"pass\",\"Package\":\"x/extra\"}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := mergeGoCoverageShardEvents(paths, filepath.Join(root, "merged"))
			if (err != nil) != tc.bad {
				t.Fatalf("event merge error=%v want bad=%v", err, tc.bad)
			}
		})
	}
}

func TestGoCoverageShardsFailureDoesNotPublishMergedEvidence(t *testing.T) {
	root := t.TempDir()
	moduleDir := filepath.Join(root, "module")
	writeCoverageShardFixture(t, moduleDir)
	testPath := filepath.Join(moduleDir, "rtk-cloud", "fixture_test.go")
	source := readTestFile(t, testPath)
	source = strings.Replace(source, "if first()!=1", "if first()==1", 1)
	if err := os.WriteFile(testPath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "coverage.out")
	events := filepath.Join(root, "events.json")
	for _, path := range []string{profile, events} {
		if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := runGoCoverageShards(context.Background(), moduleDir, filepath.Join(root, "coverage.log"), events, profile, map[string]string{"GOWORK": "off"}, []string{"test", "-json", "-count=1", "-timeout=1m"})
	if err == nil {
		t.Fatal("failing child passed")
	}
	for _, path := range []string{profile, events} {
		if exists(path) {
			t.Fatalf("failed child published merged evidence %s", path)
		}
	}
	if log := readTestFile(t, filepath.Join(root, "coverage.log")); !strings.Contains(log, "first") {
		t.Fatalf("failed child log missing: %s", log)
	}
}

func TestGoCoverageShardsCancellationJoinsRunningTestProcesses(t *testing.T) {
	root := t.TempDir()
	moduleDir := filepath.Join(root, "module")
	writeCoverageShardFixture(t, moduleDir)
	marker := filepath.Join(root, "child-pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runGoCoverageShards(ctx, moduleDir, filepath.Join(root, "coverage.log"), filepath.Join(root, "events.json"), filepath.Join(root, "coverage.out"), map[string]string{"GOWORK": "off", "COVERAGE_FIXTURE_BLOCK": marker}, []string{"test", "-json", "-count=1", "-timeout=1m"})
	}()
	deadline := time.Now().Add(20 * time.Second)
	for !exists(marker) || !exists(marker+".nested") {
		select {
		case err := <-done:
			t.Fatalf("runner stopped before child started: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	nestedPID, err := strconv.Atoi(strings.TrimSpace(readTestFile(t, marker+".nested")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-nestedPID, syscall.SIGKILL) })
	pid, err := strconv.Atoi(strings.TrimSpace(readTestFile(t, marker)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled active tests passed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not join running children")
	}
	deadline = time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil || syscall.Kill(nestedPID, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child test process %d or detached observer %d survived cancellation", pid, nestedPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if exists(filepath.Join(root, "coverage.out")) {
		t.Fatal("canceled run published profile")
	}
}

func TestGoCoverageShardCancellationDiscoveryFailuresStillKillOwnedProcesses(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"malformed", "#!/bin/sh\nprintf 'not-a-pid\\n'\n"},
		{"command failure", "#!/bin/sh\nexit 2\n"},
		{"timeout", "#!/bin/sh\nexec /bin/sleep 60\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			pgrep := filepath.Join(root, "pgrep")
			if err := os.WriteFile(pgrep, []byte(tc.script), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", "sleep 60")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
			if err := killGoCoverageProcessTree(cmd.Process.Pid, pgrep); err == nil {
				t.Fatal("discovery failure was ignored")
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("owned process exited successfully despite forced cancellation")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("discovery error left owned processes stopped or running")
			}
		})
	}
	t.Run("missing pgrep fails before launch", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("PATH", root)
		err := runGoCoverageShardCommand(context.Background(), root, filepath.Join(root, "log"), filepath.Join(root, "events"), nil, nil)
		if err == nil || !strings.Contains(err.Error(), "requires pgrep") {
			t.Fatalf("missing dependency error=%v", err)
		}
		if exists(filepath.Join(root, "log")) {
			t.Fatal("runner opened evidence before checking cancellation prerequisite")
		}
		err = runGoCoverageShards(context.Background(), root, filepath.Join(root, "log"), filepath.Join(root, "events"), filepath.Join(root, "profile"), nil, nil)
		if err == nil || !strings.Contains(err.Error(), "requires pgrep") {
			t.Fatalf("outer runner masked prerequisite error: %v", err)
		}
	})
}
