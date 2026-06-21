package home100k

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultDeviceCount      = 100000
	DefaultUserCount        = 5000
	DefaultDevicesPerUser   = 20
	DefaultVMCount          = 5
	DefaultServerTarget     = "staging/lke"
	DefaultLoadGeneratorRun = "ephemeral-linode-vm"
	DefaultRunnerNofile     = 1048576
	DefaultDeviceSession    = "lifetime-subscription"
	DefaultRunnerReadModel  = "go-netpoll-bounded-reader-goroutine"
	DefaultStageWarmUp      = "1m"
	DefaultStageSteady      = "2m"
	DefaultStageCoolDown    = "45s"
	DefaultScenarioProfile  = "home-diverse-v1"
)

type PlanOptions struct {
	EnvRoot         string `json:"env_root"`
	Brandname       string `json:"brandname"`
	Region          string `json:"region"`
	DeviceCount     int    `json:"device_count,omitempty"`
	UserCount       int    `json:"user_count,omitempty"`
	DevicesPerUser  int    `json:"devices_per_user,omitempty"`
	VMCount         int    `json:"vm_count,omitempty"`
	StageWarmUp     string `json:"-"`
	StageSteady     string `json:"-"`
	StageCoolDown   string `json:"-"`
	RunnerNofile    int    `json:"runner_nofile_limit,omitempty"`
	SessionModel    string `json:"device_session_model,omitempty"`
	RunnerReadModel string `json:"runner_read_model,omitempty"`
	ScenarioProfile string `json:"scenario_profile,omitempty"`
}

type Plan struct {
	Conditions        TestConditions           `json:"conditions"`
	ScenarioProfile   string                   `json:"scenario_profile"`
	DeviceMix         map[string]int           `json:"device_mix"`
	DeviceProfiles    map[string]DeviceProfile `json:"device_profiles"`
	UserProfiles      map[string]UserProfile   `json:"user_profiles"`
	PresenceMix       map[string]int           `json:"presence_mix"`
	Target            TargetWindow             `json:"target"`
	Stages            []Stage                  `json:"-"`
	Shards            []Shard                  `json:"shards"`
	Assignments       []VMAssignment           `json:"vm_assignments"`
	Lifecycle         []LifecycleAction        `json:"lifecycle_actions"`
	Workflow          []string                 `json:"workflow"`
	Artifacts         Artifacts                `json:"artifacts"`
	CleanupPlan       []string                 `json:"cleanup_plan"`
}

type TargetWindow struct {
	TargetConnects int    `json:"target_connects"`
	RampUpTime     string `json:"ramp_up_time"`
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
	VMCount              int    `json:"vm_count"`
	RunnerNofileLimit    int    `json:"runner_nofile_limit"`
	DeviceSessionModel   string `json:"device_session_model"`
	RunnerReadModel      string `json:"runner_read_model"`
}

type Stage struct {
	Name             string `json:"name"`
	ConnectedDevices int    `json:"connected_devices"`
	WarmUp           string `json:"warm_up"`
	SteadyState      string `json:"steady_state"`
	CoolDown         string `json:"cool_down"`
}

func (s Stage) MarshalJSON() ([]byte, error) {
	type stagePlanJSON struct {
		Name           string `json:"name"`
		TargetConnects int    `json:"target_connects"`
		RampUpTime     string `json:"ramp_up_time"`
	}
	return json.Marshal(stagePlanJSON{
		Name:           s.Name,
		TargetConnects: s.ConnectedDevices,
		RampUpTime:     s.WarmUp,
	})
}

type DeviceProfile struct {
	RatioWeight    int    `json:"ratio_weight"`
	TrafficProfile string `json:"traffic_profile"`
	PayloadClass   string `json:"payload_class"`
}

type UserProfile struct {
	RatioWeight   int    `json:"ratio_weight"`
	ActionProfile string `json:"action_profile"`
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
	devices := opts.DeviceCount
	if devices <= 0 {
		devices = DefaultDeviceCount
	}
	devicesPerUser := opts.DevicesPerUser
	if devicesPerUser <= 0 {
		devicesPerUser = DefaultDevicesPerUser
	}
	users := opts.UserCount
	if users <= 0 {
		if opts.DeviceCount > 0 {
			users = ceilDiv(devices, devicesPerUser)
		} else {
			users = DefaultUserCount
		}
	}
	if devices <= 0 {
		return Plan{}, fmt.Errorf("device count must be positive, got %d", devices)
	}
	if users <= 0 {
		return Plan{}, fmt.Errorf("user count must be positive, got %d", users)
	}
	if devicesPerUser <= 0 {
		return Plan{}, fmt.Errorf("devices per user must be positive, got %d", devicesPerUser)
	}
	vmCount := opts.VMCount
	if vmCount <= 0 {
		vmCount = DefaultVMCount
	}
	if vmCount <= 0 {
		return Plan{}, fmt.Errorf("VM count must be positive, got %d", vmCount)
	}
	runnerNofile := opts.RunnerNofile
	if runnerNofile <= 0 {
		runnerNofile = DefaultRunnerNofile
	}
	sessionModel := strings.TrimSpace(opts.SessionModel)
	if sessionModel == "" {
		sessionModel = DefaultDeviceSession
	}
	readModel := strings.TrimSpace(opts.RunnerReadModel)
	if readModel == "" {
		readModel = DefaultRunnerReadModel
	}
	scenarioProfile := strings.TrimSpace(opts.ScenarioProfile)
	if scenarioProfile == "" {
		scenarioProfile = DefaultScenarioProfile
	}

		stages := stagePlan(devices, opts.StageWarmUp, opts.StageSteady, opts.StageCoolDown)
		plan := Plan{
			Conditions: TestConditions{
				EnvRoot:              opts.EnvRoot,
				Brandname:            opts.Brandname,
			Region:               opts.Region,
			Devices:              devices,
			Users:                users,
			DevicesPerUser:       devicesPerUser,
			ServerTarget:         DefaultServerTarget,
			LoadGeneratorRuntime: DefaultLoadGeneratorRun,
			FirstBaselineRegion:  "single-region",
			DeviceGeneratorLimit: ceilDiv(devices, vmCount),
			VMCount:              vmCount,
			RunnerNofileLimit:    runnerNofile,
			DeviceSessionModel:   sessionModel,
			RunnerReadModel:      readModel,
		},
		ScenarioProfile:   scenarioProfile,
		DeviceMix:         proportionalMix(devices, homeDiverseDeviceMixBuckets()),
			DeviceProfiles:    homeDiverseDeviceProfiles(),
			UserProfiles:      homeDiverseUserProfiles(),
			PresenceMix:       proportionalMix(devices, []ratioBucket{{Name: "online_steady", Weight: 85}, {Name: "offline_desired_queue", Weight: 10}, {Name: "flapping_reconnect", Weight: 5}}),
			Target:            targetWindowFromStages(stages),
			Stages:            stages,
			Workflow:          []string{"plan", "provision-vms", "sync", "run-stages", "collect", "collect-server-evidence", "aggregate", "destroy-vms"},
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
	plan.Shards = append(plan.Shards, deviceShards(opts.Region, devices, vmCount)...)
	plan.Shards = append(plan.Shards, userShards(opts.Region, users, vmCount)...)
	plan.Assignments = mixedAssignments(opts.Region, plan.Shards, vmCount)
	plan.Lifecycle = BuildLifecycleActions(plan, "<run_id>")
	return plan, nil
}

func homeDiverseDeviceMixBuckets() []ratioBucket {
	return []ratioBucket{
		{Name: "light", Weight: 18},
		{Name: "switch", Weight: 7},
		{Name: "smart_plug", Weight: 12},
		{Name: "air_conditioner", Weight: 10},
		{Name: "environment_sensor", Weight: 12},
		{Name: "security_sensor", Weight: 10},
		{Name: "smart_meter", Weight: 8},
		{Name: "camera_status", Weight: 7},
		{Name: "door_lock", Weight: 4},
		{Name: "appliance", Weight: 7},
		{Name: "gateway", Weight: 5},
	}
}

func homeDiverseDeviceProfiles() map[string]DeviceProfile {
	return map[string]DeviceProfile{
		"light":              {RatioWeight: 18, TrafficProfile: "command_heavy", PayloadClass: "power"},
		"switch":             {RatioWeight: 7, TrafficProfile: "command_heavy", PayloadClass: "power"},
		"smart_plug":         {RatioWeight: 12, TrafficProfile: "command_heavy", PayloadClass: "power_energy"},
		"air_conditioner":    {RatioWeight: 10, TrafficProfile: "hvac_slow_converge", PayloadClass: "hvac"},
		"environment_sensor": {RatioWeight: 12, TrafficProfile: "periodic_reported", PayloadClass: "environment"},
		"security_sensor":    {RatioWeight: 10, TrafficProfile: "event_burst", PayloadClass: "security"},
		"smart_meter":        {RatioWeight: 8, TrafficProfile: "periodic_reported", PayloadClass: "energy"},
		"camera_status":      {RatioWeight: 7, TrafficProfile: "event_burst", PayloadClass: "camera_status"},
		"door_lock":          {RatioWeight: 4, TrafficProfile: "strict_access", PayloadClass: "lock"},
		"appliance":          {RatioWeight: 7, TrafficProfile: "state_machine", PayloadClass: "appliance"},
		"gateway":            {RatioWeight: 5, TrafficProfile: "gateway_batch_sync", PayloadClass: "gateway"},
	}
}

func homeDiverseUserProfiles() map[string]UserProfile {
	return map[string]UserProfile{
		"owner_admin":    {RatioWeight: 15, ActionProfile: "scene_command"},
		"daily_user":     {RatioWeight: 45, ActionProfile: "single_device_command"},
		"background_app": {RatioWeight: 25, ActionProfile: "open_home_refresh"},
		"automation":     {RatioWeight: 15, ActionProfile: "automation_command"},
	}
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

type ratioBucket struct {
	Name   string
	Weight int
}

func proportionalMix(total int, buckets []ratioBucket) map[string]int {
	out := make(map[string]int, len(buckets))
	if total <= 0 || len(buckets) == 0 {
		return out
	}
	weightTotal := 0
	for _, bucket := range buckets {
		weightTotal += bucket.Weight
	}
	assigned := 0
	for idx, bucket := range buckets {
		value := 0
		if idx == len(buckets)-1 {
			value = total - assigned
		} else if weightTotal > 0 {
			value = total * bucket.Weight / weightTotal
		}
		out[bucket.Name] = value
		assigned += value
	}
	return out
}

func stagePlan(devices int, warmUp string, steady string, coolDown string) []Stage {
	return []Stage{{Name: "target", ConnectedDevices: devices, WarmUp: warmUp, SteadyState: steady, CoolDown: coolDown}}
}

func targetWindowFromStages(stages []Stage) TargetWindow {
	if len(stages) == 0 {
		return TargetWindow{}
	}
	return TargetWindow{
		TargetConnects: stages[0].ConnectedDevices,
		RampUpTime:     stages[0].WarmUp,
	}
}

func deviceShards(region string, totalDevices int, vmCount int) []Shard {
	shards := []Shard{}
	base := totalDevices / vmCount
	remainder := totalDevices % vmCount
	start := 0
	for idx := 0; idx < vmCount; idx++ {
		count := base
		if idx < remainder {
			count++
		}
		end := start + count
		shards = append(shards, Shard{
			Role:   "device-mqtt",
			Index:  idx,
			Region: region,
			Start:  start,
			End:    end,
			Count:  count,
		})
		start = end
	}
	return shards
}

func mixedAssignments(region string, shards []Shard, vmCount int) []VMAssignment {
	assignments := make([]VMAssignment, 0, vmCount)
	for idx := 0; idx < vmCount; idx++ {
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

func userShards(region string, totalUsers int, vmCount int) []Shard {
	shards := []Shard{}
	base := totalUsers / vmCount
	remainder := totalUsers % vmCount
	start := 0
	for idx := 0; idx < vmCount; idx++ {
		count := base
		if idx < remainder {
			count++
		}
		end := start + count
		shards = append(shards, Shard{
			Role:   "user-app",
			Index:  idx,
			Region: region,
			Start:  start,
			End:    end,
			Count:  count,
		})
		start = end
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
	if p.Conditions.VMCount <= 0 {
		return fmt.Errorf("VM count must be positive, got %d", p.Conditions.VMCount)
	}
	if len(p.ShardsByRole("device-mqtt")) != p.Conditions.VMCount {
		return fmt.Errorf("100K mixed baseline requires %d device-mqtt shards, got %d", p.Conditions.VMCount, len(p.ShardsByRole("device-mqtt")))
	}
	if len(p.Assignments) != p.Conditions.VMCount {
		return fmt.Errorf("100K mixed baseline requires %d VM assignments, got %d", p.Conditions.VMCount, len(p.Assignments))
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

func ceilDiv(value int, divisor int) int {
	if divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}
