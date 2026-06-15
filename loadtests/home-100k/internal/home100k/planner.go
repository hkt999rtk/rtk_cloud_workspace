package home100k

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultDeviceCount      = 100000
	DefaultUserCount        = 5000
	DefaultDevicesPerUser   = 10
	DefaultDevicesPerVM     = 10000
	DefaultUserShards       = 10
	DefaultServerTarget     = "staging/lke"
	DefaultLoadGeneratorRun = "ephemeral-linode-vm"
	DefaultStageWarmUp      = "5m"
	DefaultStageSteady      = "15m"
	DefaultStageCoolDown    = "3m"
)

type PlanOptions struct {
	EnvRoot       string `json:"env_root"`
	Brandname     string `json:"brandname"`
	Region        string `json:"region"`
	StageWarmUp   string `json:"stage_warm_up"`
	StageSteady   string `json:"stage_steady"`
	StageCoolDown string `json:"stage_cool_down"`
}

type Plan struct {
	Conditions  TestConditions    `json:"conditions"`
	DeviceMix   map[string]int    `json:"device_mix"`
	PresenceMix map[string]int    `json:"presence_mix"`
	Stages      []Stage           `json:"stages"`
	Shards      []Shard           `json:"shards"`
	Assignments []VMAssignment    `json:"vm_assignments"`
	Lifecycle   []LifecycleAction `json:"lifecycle_actions"`
	Workflow    []string          `json:"workflow"`
	Artifacts   Artifacts         `json:"artifacts"`
	CleanupPlan []string          `json:"cleanup_plan"`
}

type TestConditions struct {
	EnvRoot              string `json:"env_root"`
	Brandname            string `json:"brandname"`
	Region               string `json:"region"`
	Devices              int    `json:"devices"`
	Users                int    `json:"users"`
	DevicesPerUser       int    `json:"devices_per_user"`
	ServerTarget         string `json:"server_target"`
	LoadGeneratorRuntime string `json:"load_generator_runtime"`
	FirstBaselineRegion  string `json:"first_baseline_region_model"`
	DeviceGeneratorLimit int    `json:"device_generator_density"`
}

type Stage struct {
	Name             string `json:"name"`
	ConnectedDevices int    `json:"connected_devices"`
	WarmUp           string `json:"warm_up"`
	SteadyState      string `json:"steady_state"`
	CoolDown         string `json:"cool_down"`
}

type Shard struct {
	Role   string `json:"role"`
	Index  int    `json:"index"`
	Region string `json:"region"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Count  int    `json:"count"`
}

type VMAssignment struct {
	Label      string  `json:"label"`
	Index      int     `json:"index"`
	Role       string  `json:"role"`
	Region     string  `json:"region"`
	TaskShards []Shard `json:"task_shards"`
}

type Artifacts struct {
	RunPlan         string `json:"run_plan"`
	ShardResults    string `json:"shard_results"`
	AggregateReport string `json:"aggregate_report"`
	ServerEvidence  string `json:"server_evidence"`
}

func NewPlan(opts PlanOptions) (Plan, error) {
	opts.EnvRoot = strings.TrimSpace(opts.EnvRoot)
	opts.Brandname = strings.TrimSpace(opts.Brandname)
	opts.Region = strings.TrimSpace(opts.Region)
	opts.StageWarmUp = defaultDuration(opts.StageWarmUp, DefaultStageWarmUp)
	opts.StageSteady = defaultDuration(opts.StageSteady, DefaultStageSteady)
	opts.StageCoolDown = defaultDuration(opts.StageCoolDown, DefaultStageCoolDown)
	if opts.EnvRoot == "" {
		return Plan{}, errors.New("--env-root is required")
	}
	if opts.Brandname == "" {
		return Plan{}, errors.New("--brandname is required")
	}
	if opts.Region == "" {
		return Plan{}, errors.New("--region is required")
	}
	if err := validateDuration("stage warm-up", opts.StageWarmUp); err != nil {
		return Plan{}, err
	}
	if err := validateDuration("stage steady", opts.StageSteady); err != nil {
		return Plan{}, err
	}
	if err := validateDuration("stage cool-down", opts.StageCoolDown); err != nil {
		return Plan{}, err
	}

	plan := Plan{
		Conditions: TestConditions{
			EnvRoot:              opts.EnvRoot,
			Brandname:            opts.Brandname,
			Region:               opts.Region,
			Devices:              DefaultDeviceCount,
			Users:                DefaultUserCount,
			DevicesPerUser:       DefaultDevicesPerUser,
			ServerTarget:         DefaultServerTarget,
			LoadGeneratorRuntime: DefaultLoadGeneratorRun,
			FirstBaselineRegion:  "single-region",
			DeviceGeneratorLimit: DefaultDevicesPerVM,
		},
		DeviceMix: map[string]int{
			"light":           50000,
			"air_conditioner": 20000,
			"smart_meter":     30000,
		},
		PresenceMix: map[string]int{
			"online_steady":         85000,
			"offline_desired_queue": 10000,
			"flapping_reconnect":    5000,
		},
		Stages: []Stage{
			{Name: "25k", ConnectedDevices: 25000, WarmUp: opts.StageWarmUp, SteadyState: opts.StageSteady, CoolDown: opts.StageCoolDown},
			{Name: "50k", ConnectedDevices: 50000, WarmUp: opts.StageWarmUp, SteadyState: opts.StageSteady, CoolDown: opts.StageCoolDown},
			{Name: "75k", ConnectedDevices: 75000, WarmUp: opts.StageWarmUp, SteadyState: opts.StageSteady, CoolDown: opts.StageCoolDown},
			{Name: "100k", ConnectedDevices: 100000, WarmUp: opts.StageWarmUp, SteadyState: opts.StageSteady, CoolDown: opts.StageCoolDown},
		},
		Workflow: []string{"plan", "provision-vms", "sync", "run-stages", "collect", "collect-server-evidence", "aggregate", "destroy-vms"},
		Artifacts: Artifacts{
			RunPlan:         "loadtests/home-100k/plans/<run_id>/plan.json",
			ShardResults:    "loadtests/home-100k/reports/<run_id>/shards/",
			AggregateReport: "loadtests/home-100k/reports/<run_id>/TEST_REPORT.md",
			ServerEvidence:  "loadtests/home-100k/reports/<run_id>/server-evidence.json",
		},
		CleanupPlan: []string{
			"tag all ephemeral Linode VMs with home-100k run_id",
			"collect results before deleting VMs",
			"list leftover VMs by run_id when cleanup is interrupted",
			"destroy leftover VMs by run_id after operator confirmation",
		},
	}
	plan.Shards = append(plan.Shards, deviceShards(opts.Region)...)
	plan.Shards = append(plan.Shards, userShards(opts.Region)...)
	plan.Assignments = mixedAssignments(opts.Region, plan.Shards)
	plan.Lifecycle = BuildLifecycleActions(plan, "<run_id>")
	return plan, nil
}

func defaultDuration(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func validateDuration(label string, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s duration %q is invalid: %w", label, value, err)
	}
	if duration <= 0 {
		return fmt.Errorf("%s duration must be positive, got %q", label, value)
	}
	return nil
}

func deviceShards(region string) []Shard {
	shards := []Shard{}
	for idx := 0; idx < DefaultDeviceCount/DefaultDevicesPerVM; idx++ {
		start := idx * DefaultDevicesPerVM
		end := start + DefaultDevicesPerVM
		shards = append(shards, Shard{
			Role:   "device-mqtt",
			Index:  idx,
			Region: region,
			Start:  start,
			End:    end,
			Count:  end - start,
		})
	}
	return shards
}

func mixedAssignments(region string, shards []Shard) []VMAssignment {
	assignments := make([]VMAssignment, 0, DefaultDeviceCount/DefaultDevicesPerVM)
	for idx := 0; idx < DefaultDeviceCount/DefaultDevicesPerVM; idx++ {
		tasks := []Shard{}
		if shard, ok := findShardInList(shards, "device-mqtt", idx); ok {
			tasks = append(tasks, shard)
		}
		if shard, ok := findShardInList(shards, "user-app", idx); ok {
			tasks = append(tasks, shard)
		}
		assignments = append(assignments, VMAssignment{
			Label:      fmt.Sprintf("home-100k-mixed-%03d", idx),
			Index:      idx,
			Role:       "mixed",
			Region:     region,
			TaskShards: tasks,
		})
	}
	return assignments
}

func findShardInList(shards []Shard, role string, index int) (Shard, bool) {
	for _, shard := range shards {
		if shard.Role == role && shard.Index == index {
			return shard, true
		}
	}
	return Shard{}, false
}

func userShards(region string) []Shard {
	shards := []Shard{}
	usersPerShard := DefaultUserCount / DefaultUserShards
	for idx := 0; idx < DefaultUserShards; idx++ {
		start := idx * usersPerShard
		end := start + usersPerShard
		shards = append(shards, Shard{
			Role:   "user-app",
			Index:  idx,
			Region: region,
			Start:  start,
			End:    end,
			Count:  end - start,
		})
	}
	return shards
}

func (p Plan) ShardsByRole(role string) []Shard {
	out := []Shard{}
	for _, shard := range p.Shards {
		if shard.Role == role {
			out = append(out, shard)
		}
	}
	return out
}

func (p Plan) Validate() error {
	if p.Conditions.Devices != sumMap(p.DeviceMix) {
		return fmt.Errorf("device mix sums to %d, want %d", sumMap(p.DeviceMix), p.Conditions.Devices)
	}
	if p.Conditions.Devices != sumMap(p.PresenceMix) {
		return fmt.Errorf("presence mix sums to %d, want %d", sumMap(p.PresenceMix), p.Conditions.Devices)
	}
	if len(p.ShardsByRole("device-mqtt")) < 10 {
		return errors.New("100K baseline requires at least 10 device-mqtt shards")
	}
	if len(p.Assignments) != 10 {
		return fmt.Errorf("100K mixed baseline requires 10 VM assignments, got %d", len(p.Assignments))
	}
	return nil
}

func sumMap(values map[string]int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}
