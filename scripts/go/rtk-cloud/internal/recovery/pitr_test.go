package recovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pitrControlFixture = `max_connections setting:              100
max_worker_processes setting:         8
max_wal_senders setting:              10
max_prepared_xacts setting:           0
max_locks_per_xact setting:           64
`

func TestPITRTargetAndIsolation(t *testing.T) {
	c := baseConfigFixture(t)
	plan := PITRPlan{Version: 1, TargetLSN: "0/3000100", TargetTimeline: 1, Executable: "/private/tool", WALConfigFile: "/private/wal.json"}
	config, err := renderPITRConfig(c.WAL, plan, "/private/restore", "/private/identity", pitrControlFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"listen_addresses = ''", "hot_standby = on", "archive_mode = off", "recovery_target_action = 'pause'", "recovery_target_timeline = '1'", "recovery_target_lsn = '0/3000100'", "max_prepared_transactions = 0", "--name ''%f'' --destination ''%p''"} {
		if !strings.Contains(config, want) {
			t.Fatal("missing isolated target setting", want)
		}
	}
	for _, mutate := range []func(*PITRPlan){func(p *PITRPlan) { p.TargetLSN = "0/0" }, func(p *PITRPlan) { p.TargetLSN = "latest" }, func(p *PITRPlan) { p.TargetTimeline = 0 }, func(p *PITRPlan) { p.Executable = "relative" }, func(p *PITRPlan) { p.WALConfigFile = "/tmp/line\nnew" }} {
		bad := plan
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid target accepted")
		}
	}
	for _, control := range []string{"", pitrControlFixture + "WARNING: bad checksum\n", strings.ReplaceAll(pitrControlFixture, "100", "99999999999999999999")} {
		if _, err = renderPITRConfig(c.WAL, plan, "/private/restore", "/private/identity", control); err == nil {
			t.Fatal("invalid control data accepted")
		}
	}
}
func TestPITRStaticShellArguments(t *testing.T) {
	// Simulate PostgreSQL percent expansion, then let the actual shell parse the
	// static argument. Dollars, backticks, quotes and percent placeholders stay data.
	value := "/private/a 'quoted' %f $(touch SHOULD_NOT_EXIST) `touch SHOULD_NOT_EXIST` \\ tail"
	quoted := strings.ReplaceAll(restoreShellArgument(value), "%%", "%")
	out, err := exec.Command("/bin/sh", "-c", "printf '%s' "+quoted).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != value {
		t.Fatalf("shell argument changed: %q", out)
	}
	if _, err = os.Lstat("SHOULD_NOT_EXIST"); !os.IsNotExist(err) {
		t.Fatal("static path executed a shell command")
	}
}
func TestPITRInputConfigurationBinding(t *testing.T) {
	c := baseConfigFixture(t)
	dir := filepath.Dir(c.ServiceFile)
	tool := filepath.Join(dir, "tool")
	if err := os.WriteFile(tool, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "identity")
	if err := os.WriteFile(key, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "wal.json")
	if err := WriteJSON(config, c.WAL); err != nil {
		t.Fatal(err)
	}
	p := PITRPlan{Version: 1, TargetLSN: "0/3000100", TargetTimeline: 1, Executable: tool, WALConfigFile: config}
	if err := p.checkInputs(c.WAL, key); err != nil {
		t.Fatal(err)
	}
	wrong := c.WAL
	wrong.SystemIdentifier = "99"
	if err := p.checkInputs(wrong, key); err == nil {
		t.Fatal("unrelated WAL config accepted")
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if err := p.checkInputs(c.WAL, key); err == nil {
		t.Fatal("public age identity accepted")
	}
}
