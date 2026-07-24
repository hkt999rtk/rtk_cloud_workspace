package home100k

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultDeviceCount               = 100000
	DefaultUserCount                 = 5000
	DefaultDevicesPerUser            = 20
	DefaultLoadGeneratorDevicesPerVM = 20000
	DefaultDevicesPerVM              = DefaultLoadGeneratorDevicesPerVM
	DefaultVMCount                   = 5
	DefaultUserShards                = DefaultVMCount
	DefaultServerTarget              = "environment"
	DefaultLoadGeneratorRun          = "ephemeral-linode-vm"
	DefaultRunnerNofile              = 1048576
	DefaultDeviceSession             = "lifetime-subscription"
	DefaultRunnerReadModel           = "go-netpoll-bounded-reader-goroutine"
	DefaultDeviceTokenRequestTimeout = "10s"
	DefaultStageWarmUp               = "30s"
	DefaultStageSteady               = "90s"
	DefaultStageCoolDown             = "30s"
	DefaultScenarioProfile           = "home-diverse-v1"
	VideoCanaryScenarioProfile       = "video-canary-v1"
	Video1KScenarioProfile           = "video-1k-v1"
	ClipStorageCanaryScenarioProfile = "clip-storage-canary-v1"
	ClipStorage1KScenarioProfile     = "clip-storage-1k-v1"
	ClipStorage10KScenarioProfile    = "clip-storage-10k-v2"
	Video50KTurnScenarioProfile      = "video-50k-turn-v1"
	Video100KTurnScenarioProfile     = "video-100k-turn-v1"
	DefaultVideo1KDevices            = 1000
	DefaultVideo1KViewers            = 100
	DefaultVideo1KMediaSet           = "h264"
	DefaultVideo50KDevices           = 50000
	DefaultVideo50KViewers           = 5000
	DefaultVideo100KDevices          = 100000
	DefaultVideo100KViewers          = 5000
	DefaultVideo100KLadder           = "100,500,1000,2000,5000"
	DefaultVideo100KStepDuration     = "10s"
	DefaultVideo100KStepCooldown     = "2m"
	DefaultVMLabelPrefix             = "lg"

	DefaultFunctionalSuccessThresholdPercent    = 99.5
	DefaultClientTargetCompletenessPercent      = 100.0
	DefaultExactEventCorrelationPercent         = 100.0
	DefaultAggregateCorrelationTolerancePercent = 0.1
	DefaultAggregateCorrelationMinTolerance     = 5
)

type PlanOptions struct {
	EnvRoot                              string  `json:"env_root"`
	Brandname                            string  `json:"brandname"`
	BrandPlanFile                        string  `json:"brand_plan_file,omitempty"`
	Region                               string  `json:"region"`
	DeviceCount                          int     `json:"device_count,omitempty"`
	UserCount                            int     `json:"user_count,omitempty"`
	DevicesPerUser                       int     `json:"devices_per_user,omitempty"`
	VMCount                              int     `json:"vm_count,omitempty"`
	VideoGeneratorVMCount                int     `json:"video_generator_vm_count,omitempty"`
	VideoGeneratorLabelPrefix            string  `json:"video_generator_label_prefix,omitempty"`
	LoadGeneratorDevicesPerVM            int     `json:"load_generator_devices_per_vm,omitempty"`
	StageWarmUp                          string  `json:"stage_warm_up"`
	StageSteady                          string  `json:"stage_steady"`
	StageCoolDown                        string  `json:"stage_cool_down"`
	RunnerNofile                         int     `json:"runner_nofile_limit,omitempty"`
	SessionModel                         string  `json:"device_session_model,omitempty"`
	RunnerReadModel                      string  `json:"runner_read_model,omitempty"`
	DeviceTokenRequestTimeout            string  `json:"device_token_request_timeout,omitempty"`
	DeviceTokenRequestRetries            int     `json:"device_token_request_retries,omitempty"`
	ScenarioProfile                      string  `json:"scenario_profile,omitempty"`
	VMLabelPrefix                        string  `json:"vm_label_prefix,omitempty"`
	FunctionalSuccessThresholdPercent    float64 `json:"functional_success_threshold_percent,omitempty"`
	ClientTargetCompletenessPercent      float64 `json:"client_target_completeness_percent,omitempty"`
	ExactEventCorrelationPercent         float64 `json:"exact_event_correlation_percent,omitempty"`
	AggregateCorrelationTolerancePercent float64 `json:"aggregate_correlation_tolerance_percent,omitempty"`
	AggregateCorrelationMinTolerance     int64   `json:"aggregate_correlation_min_tolerance,omitempty"`
}

type Plan struct {
	Conditions         TestConditions           `json:"conditions"`
	BrandDistribution  []BrandDistributionEntry `json:"brand_distribution,omitempty"`
	ScenarioProfile    string                   `json:"scenario_profile"`
	VideoProfile       VideoProfile             `json:"video_profile,omitempty"`
	ClipStorageProfile ClipStorageProfile       `json:"clip_storage_profile,omitempty"`
	DeviceMix          map[string]int           `json:"device_mix"`
	DeviceProfiles     map[string]DeviceProfile `json:"device_profiles"`
	UserProfiles       map[string]UserProfile   `json:"user_profiles"`
	StageUsageWindows  []string                 `json:"stage_usage_windows,omitempty"`
	PresenceMix        map[string]int           `json:"presence_mix"`
	Target             TargetWindow             `json:"target"`
	Stages             []Stage                  `json:"stages"`
	Shards             []Shard                  `json:"shards"`
	Assignments        []VMAssignment           `json:"vm_assignments"`
	Lifecycle          []LifecycleAction        `json:"lifecycle_actions"`
	Workflow           []string                 `json:"workflow"`
	Artifacts          Artifacts                `json:"artifacts"`
	CleanupPlan        []string                 `json:"cleanup_plan"`
}

type VideoProfile struct {
	Name            string `json:"name,omitempty"`
	VideoDevices    int    `json:"video_devices,omitempty"`
	VideoViewers    int    `json:"video_viewers,omitempty"`
	ViewerLadder    []int  `json:"viewer_ladder,omitempty"`
	WebRTCMediaSet  string `json:"webrtc_media_set,omitempty"`
	WebRTCICEPolicy string `json:"webrtc_ice_policy,omitempty"`
	StepDuration    string `json:"step_duration,omitempty"`
	StepCooldown    string `json:"step_cooldown,omitempty"`
	TURNTransport   string `json:"turn_transport,omitempty"`
	MediaSecurity   string `json:"media_security,omitempty"`
	SignalingLayer  string `json:"signaling_layer,omitempty"`
	MediaLayer      string `json:"media_layer,omitempty"`
	DeviceActorRole string `json:"device_actor_role,omitempty"`
	AppActorRole    string `json:"app_actor_role,omitempty"`
	ViewerActorRole string `json:"viewer_actor_role,omitempty"`
}

type ClipStorageProfile struct {
	Name                 string `json:"name,omitempty"`
	CameraDevices        int    `json:"camera_devices,omitempty"`
	ClipsPerCameraPerDay int    `json:"clips_per_camera_per_day,omitempty"`
	ScheduleWindow       string `json:"schedule_window,omitempty"`
	PoissonSeed          int64  `json:"poisson_seed,omitempty"`
	UploadConcurrency    int    `json:"upload_concurrency,omitempty"`
	Fixture              string `json:"fixture,omitempty"`
	Thumbnail            string `json:"thumbnail,omitempty"`
}

func (p Plan) VideoEnabled() bool {
	return strings.TrimSpace(p.VideoProfile.Name) != ""
}

type TargetWindow struct {
	TargetConnects int    `json:"target_connects"`
	RampUpTime     string `json:"ramp_up_time"`
}

type TestConditions struct {
	EnvRoot                              string  `json:"env_root"`
	Brandname                            string  `json:"brandname"`
	BrandPlanFile                        string  `json:"brand_plan_file,omitempty"`
	Region                               string  `json:"region"`
	Devices                              int     `json:"devices"`
	Users                                int     `json:"users"`
	DeveloperUsers                       int     `json:"developer_users,omitempty"`
	DevicesPerUser                       int     `json:"devices_per_user"`
	ServerTarget                         string  `json:"server_target"`
	LoadGeneratorRuntime                 string  `json:"load_generator_runtime"`
	FirstBaselineRegion                  string  `json:"first_baseline_region_model"`
	DeviceGeneratorLimit                 int     `json:"device_generator_density"`
	LoadGeneratorDevicesPerVM            int     `json:"load_generator_devices_per_vm"`
	LoadGeneratorSizingFormula           string  `json:"load_generator_sizing_formula"`
	VMCount                              int     `json:"vm_count"`
	VideoGeneratorVMCount                int     `json:"video_generator_vm_count,omitempty"`
	VideoGeneratorLabelPrefix            string  `json:"video_generator_label_prefix,omitempty"`
	RunnerNofileLimit                    int     `json:"runner_nofile_limit"`
	DeviceSessionModel                   string  `json:"device_session_model"`
	RunnerReadModel                      string  `json:"runner_read_model"`
	DeviceTokenRequestTimeout            string  `json:"device_token_request_timeout"`
	DeviceTokenRequestRetries            int     `json:"device_token_request_retries"`
	VMLabelPrefix                        string  `json:"vm_label_prefix"`
	FunctionalSuccessThresholdPercent    float64 `json:"functional_success_threshold_percent"`
	ClientTargetCompletenessPercent      float64 `json:"client_target_completeness_percent"`
	ExactEventCorrelationPercent         float64 `json:"exact_event_correlation_percent"`
	AggregateCorrelationTolerancePercent float64 `json:"aggregate_correlation_tolerance_percent"`
	AggregateCorrelationMinTolerance     int64   `json:"aggregate_correlation_min_tolerance"`
}

type Stage struct {
	Name             string `json:"name"`
	ConnectedDevices int    `json:"connected_devices"`
	WarmUp           string `json:"warm_up"`
	SteadyState      string `json:"steady_state"`
	CoolDown         string `json:"cool_down"`
	UsageWindow      string `json:"usage_window,omitempty"`
}

type GateThresholds struct {
	FunctionalSuccessThresholdPercent    float64 `json:"functional_success_threshold_percent"`
	ClientTargetCompletenessPercent      float64 `json:"client_target_completeness_percent"`
	ExactEventCorrelationPercent         float64 `json:"exact_event_correlation_percent"`
	AggregateCorrelationTolerancePercent float64 `json:"aggregate_correlation_tolerance_percent"`
	AggregateCorrelationMinTolerance     int64   `json:"aggregate_correlation_min_tolerance"`
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
	brandPlanFile := strings.TrimSpace(opts.BrandPlanFile)
	brandPlan := BrandPlan{}
	if brandPlanFile != "" {
		var err error
		brandPlan, err = loadBrandPlan(brandPlanFile)
		if err != nil {
			return Plan{}, err
		}
		opts.DeviceCount = brandPlan.TotalDevices
		opts.DevicesPerUser = brandPlan.DevicesPerUser
		opts.UserCount = brandPlan.NormalUsers()
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
	scenarioProfile := strings.TrimSpace(opts.ScenarioProfile)
	if scenarioProfile == "" {
		scenarioProfile = DefaultScenarioProfile
	}
	videoProfile := videoProfileForScenario(scenarioProfile)
	if videoProfile.Name == Video1KScenarioProfile && opts.DeviceCount <= 0 && brandPlanFile == "" {
		opts.DeviceCount = DefaultVideo1KDevices
	}
	if videoProfile.Name == Video50KTurnScenarioProfile && opts.DeviceCount <= 0 && brandPlanFile == "" {
		opts.DeviceCount = DefaultVideo50KDevices
	}
	if videoProfile.Name == Video100KTurnScenarioProfile && opts.DeviceCount <= 0 && brandPlanFile == "" {
		opts.DeviceCount = DefaultVideo100KDevices
	}
	if scenarioProfile == ClipStorage10KScenarioProfile && opts.DeviceCount <= 0 && brandPlanFile == "" {
		opts.DeviceCount = 10000
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
	devicesPerVM := opts.LoadGeneratorDevicesPerVM
	if devicesPerVM <= 0 {
		devicesPerVM = DefaultDevicesPerVM
	}
	if devicesPerVM <= 0 {
		return Plan{}, fmt.Errorf("load-generator devices per VM must be positive, got %d", devicesPerVM)
	}
	vmCount := opts.VMCount
	if vmCount <= 0 {
		vmCount = ceilDiv(devices, devicesPerVM)
		if vmCount <= 0 {
			vmCount = 1
		}
	}
	if vmCount <= 0 {
		return Plan{}, fmt.Errorf("VM count must be positive, got %d", vmCount)
	}
	if perVM := ceilDiv(devices, vmCount); perVM > devicesPerVM {
		return Plan{}, fmt.Errorf("target devices %d with VM count %d needs %d devices per VM, above configured load-generator capacity %d; increase VM count or --load-generator-devices-per-vm", devices, vmCount, perVM, devicesPerVM)
	}
	videoGeneratorVMCount := opts.VideoGeneratorVMCount
	if videoGeneratorVMCount < 0 {
		return Plan{}, fmt.Errorf("video generator VM count must be non-negative, got %d", videoGeneratorVMCount)
	}
	videoGeneratorLabelPrefix := strings.TrimSpace(opts.VideoGeneratorLabelPrefix)
	if videoGeneratorLabelPrefix == "" {
		videoGeneratorLabelPrefix = "vg"
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
	deviceTokenRequestTimeout := strings.TrimSpace(opts.DeviceTokenRequestTimeout)
	if deviceTokenRequestTimeout == "" {
		deviceTokenRequestTimeout = DefaultDeviceTokenRequestTimeout
	}
	deviceTokenRequestRetries := opts.DeviceTokenRequestRetries
	if deviceTokenRequestRetries < 0 {
		return Plan{}, fmt.Errorf("device token request retries must be non-negative, got %d", deviceTokenRequestRetries)
	}
	if videoProfile.Name == VideoCanaryScenarioProfile || videoProfile.Name == Video1KScenarioProfile || isVideoTurnSizingProfile(videoProfile.Name) {
		videoProfile.VideoDevices = minInt(videoProfile.VideoDevices, devices)
		videoProfile.VideoViewers = minInt(videoProfile.VideoViewers, videoProfile.VideoDevices)
	}
	vmLabelPrefix := strings.TrimSpace(opts.VMLabelPrefix)
	if vmLabelPrefix == "" {
		vmLabelPrefix = DefaultVMLabelPrefix
	}
	thresholds, err := normalizeGateThresholds(opts)
	if err != nil {
		return Plan{}, err
	}

	stages := stagePlan(devices, opts.StageWarmUp, opts.StageSteady, opts.StageCoolDown)
	deviceMix := deviceMixForScenario(scenarioProfile, devices)
	if mix := brandPlan.AggregatedDeviceMix(); len(mix) > 0 {
		deviceMix = mix
	}

	plan := Plan{
		Conditions: TestConditions{
			EnvRoot:                              opts.EnvRoot,
			Brandname:                            opts.Brandname,
			BrandPlanFile:                        brandPlanFile,
			Region:                               opts.Region,
			Devices:                              devices,
			Users:                                users,
			DeveloperUsers:                       brandPlan.DeveloperUsers(),
			DevicesPerUser:                       devicesPerUser,
			ServerTarget:                         DefaultServerTarget,
			LoadGeneratorRuntime:                 DefaultLoadGeneratorRun,
			FirstBaselineRegion:                  "single-region",
			DeviceGeneratorLimit:                 ceilDiv(devices, vmCount),
			LoadGeneratorDevicesPerVM:            devicesPerVM,
			LoadGeneratorSizingFormula:           "vm_count = ceil(devices / load_generator_devices_per_vm)",
			VMCount:                              vmCount,
			VideoGeneratorVMCount:                videoGeneratorVMCount,
			VideoGeneratorLabelPrefix:            videoGeneratorLabelPrefix,
			RunnerNofileLimit:                    runnerNofile,
			DeviceSessionModel:                   sessionModel,
			RunnerReadModel:                      readModel,
			DeviceTokenRequestTimeout:            deviceTokenRequestTimeout,
			DeviceTokenRequestRetries:            deviceTokenRequestRetries,
			VMLabelPrefix:                        vmLabelPrefix,
			FunctionalSuccessThresholdPercent:    thresholds.FunctionalSuccessThresholdPercent,
			ClientTargetCompletenessPercent:      thresholds.ClientTargetCompletenessPercent,
			ExactEventCorrelationPercent:         thresholds.ExactEventCorrelationPercent,
			AggregateCorrelationTolerancePercent: thresholds.AggregateCorrelationTolerancePercent,
			AggregateCorrelationMinTolerance:     thresholds.AggregateCorrelationMinTolerance,
		},
		BrandDistribution:  brandPlan.Distribution(),
		ScenarioProfile:    scenarioProfile,
		VideoProfile:       videoProfile,
		ClipStorageProfile: clipStorageProfileForScenario(scenarioProfile),
		DeviceMix:          deviceMix,
		DeviceProfiles:     deviceProfilesForScenario(scenarioProfile),
		UserProfiles:       homeDiverseUserProfiles(),
		PresenceMix:        proportionalMix(devices, []ratioBucket{{Name: "online_steady", Weight: 85}, {Name: "offline_desired_queue", Weight: 10}, {Name: "flapping_reconnect", Weight: 5}}),
		Target:             targetWindowFromStages(stages),
		Stages:             stages,
		Workflow:           workflowSteps(scenarioProfile, videoProfile),
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
	plan.Assignments = mixedAssignments(opts.Region, plan.Shards, vmCount, vmLabelPrefix)
	if videoGeneratorVMCount > 0 {
		plan.Assignments = append(plan.Assignments, videoAssignments(opts.Region, videoGeneratorVMCount, videoGeneratorLabelPrefix)...)
	}
	plan.Lifecycle = BuildLifecycleActions(plan, "<run_id>")
	return plan, nil
}

func clipStorageProfileForScenario(scenario string) ClipStorageProfile {
	switch strings.TrimSpace(scenario) {
	case ClipStorageCanaryScenarioProfile:
		return ClipStorageProfile{
			Name:                 ClipStorageCanaryScenarioProfile,
			CameraDevices:        2,
			ClipsPerCameraPerDay: 2,
			ScheduleWindow:       "30s",
			PoissonSeed:          20260723,
			UploadConcurrency:    2,
			Fixture:              "e2e_test/video_cloud/load/testdata/clip_1080p_h264_3mbps_15s.mp4",
			Thumbnail:            "e2e_test/video_cloud/load/testdata/thumbnail_1080p.jpg",
		}
	case ClipStorage1KScenarioProfile:
		return ClipStorageProfile{
			Name:                 ClipStorage1KScenarioProfile,
			CameraDevices:        100,
			ClipsPerCameraPerDay: 10,
			ScheduleWindow:       "10m",
			PoissonSeed:          20260723,
			UploadConcurrency:    32,
			Fixture:              "e2e_test/video_cloud/load/testdata/clip_1080p_h264_3mbps_15s.mp4",
			Thumbnail:            "e2e_test/video_cloud/load/testdata/thumbnail_1080p.jpg",
		}
	case ClipStorage10KScenarioProfile:
		return ClipStorageProfile{
			Name:                 ClipStorage10KScenarioProfile,
			CameraDevices:        1000,
			ClipsPerCameraPerDay: 10,
			ScheduleWindow:       "30m",
			PoissonSeed:          20260719,
			UploadConcurrency:    64,
			Fixture:              "e2e_test/video_cloud/load/testdata/clip_1080p_h264_3mbps_15s.mp4",
			Thumbnail:            "e2e_test/video_cloud/load/testdata/thumbnail_1080p.jpg",
		}
	default:
		return ClipStorageProfile{}
	}
}

func videoProfileForScenario(scenario string) VideoProfile {
	switch strings.TrimSpace(scenario) {
	case VideoCanaryScenarioProfile:
		return VideoProfile{
			Name: VideoCanaryScenarioProfile, VideoDevices: 2, VideoViewers: 2,
			WebRTCMediaSet: DefaultVideo1KMediaSet, WebRTCICEPolicy: "relay",
			TURNTransport: "udp,tcp", MediaSecurity: "dtls-srtp",
			SignalingLayer: "webrtc-signaling", MediaLayer: "webrtc-media",
			DeviceActorRole: "device", AppActorRole: "app", ViewerActorRole: "viewer",
		}
	case Video1KScenarioProfile:
		return VideoProfile{
			Name:            Video1KScenarioProfile,
			VideoDevices:    DefaultVideo1KViewers,
			VideoViewers:    DefaultVideo1KViewers,
			WebRTCMediaSet:  DefaultVideo1KMediaSet,
			WebRTCICEPolicy: "relay",
			TURNTransport:   "udp,tcp",
			MediaSecurity:   "dtls-srtp",
			SignalingLayer:  "webrtc-signaling",
			MediaLayer:      "webrtc-media",
			DeviceActorRole: "device",
			AppActorRole:    "app",
			ViewerActorRole: "viewer",
		}
	case Video100KTurnScenarioProfile:
		return VideoProfile{
			Name:            Video100KTurnScenarioProfile,
			VideoDevices:    DefaultVideo100KViewers,
			VideoViewers:    DefaultVideo100KViewers,
			ViewerLadder:    []int{100, 500, 1000, 2000, 5000},
			WebRTCMediaSet:  DefaultVideo1KMediaSet,
			WebRTCICEPolicy: "relay",
			StepDuration:    DefaultVideo100KStepDuration,
			StepCooldown:    DefaultVideo100KStepCooldown,
			TURNTransport:   "udp,tcp",
			MediaSecurity:   "dtls-srtp",
			SignalingLayer:  "webrtc-signaling",
			MediaLayer:      "webrtc-media",
			DeviceActorRole: "device",
			AppActorRole:    "app",
			ViewerActorRole: "viewer",
		}
	case Video50KTurnScenarioProfile:
		return VideoProfile{
			Name:            Video50KTurnScenarioProfile,
			VideoDevices:    DefaultVideo50KViewers,
			VideoViewers:    DefaultVideo50KViewers,
			ViewerLadder:    []int{100, 500, 1000, 2000, 5000},
			WebRTCMediaSet:  DefaultVideo1KMediaSet,
			WebRTCICEPolicy: "relay",
			StepDuration:    DefaultVideo100KStepDuration,
			StepCooldown:    DefaultVideo100KStepCooldown,
			TURNTransport:   "udp,tcp",
			MediaSecurity:   "dtls-srtp",
			SignalingLayer:  "webrtc-signaling",
			MediaLayer:      "webrtc-media",
			DeviceActorRole: "device",
			AppActorRole:    "app",
			ViewerActorRole: "viewer",
		}
	default:
		return VideoProfile{}
	}
}

func isVideoTurnSizingProfile(name string) bool {
	switch strings.TrimSpace(name) {
	case Video50KTurnScenarioProfile, Video100KTurnScenarioProfile:
		return true
	default:
		return false
	}
}

func deviceMixForScenario(scenario string, devices int) map[string]int {
	switch strings.TrimSpace(scenario) {
	case VideoCanaryScenarioProfile, ClipStorageCanaryScenarioProfile:
		return map[string]int{"camera": devices}
	}
	if isVideoFeatureProfile(scenario) || isClipStorageProfile(scenario) {
		return proportionalMix(devices, []ratioBucket{
			{Name: "camera", Weight: 10},
			{Name: "light", Weight: 30},
			{Name: "air_conditioner", Weight: 30},
			{Name: "smart_meter", Weight: 30},
		})
	}
	if isVideoTurnSizingProfile(scenario) {
		return proportionalMix(devices, videoTurnSizingDeviceMixBuckets())
	}
	return proportionalMix(devices, homeDiverseDeviceMixBuckets())
}

func deviceProfilesForScenario(scenario string) map[string]DeviceProfile {
	profiles := homeDiverseDeviceProfiles()
	if isVideoFeatureProfile(scenario) || isClipStorageProfile(scenario) || isVideoTurnSizingProfile(scenario) {
		profiles["camera"] = DeviceProfile{RatioWeight: 10, TrafficProfile: "event_burst", PayloadClass: "camera_status"}
	}
	return profiles
}

func workflowSteps(scenario string, video VideoProfile) []string {
	steps := []string{"plan", "provision-vms", "sync", "run-stages", "collect"}
	if strings.TrimSpace(video.Name) != "" {
		steps = append(steps, "run-video-loadtest", "collect-video-evidence")
	}
	if isClipStorageProfile(scenario) {
		steps = append(steps, "run-clip-storage-loadtest", "collect-clip-storage-evidence")
	}
	steps = append(steps, "collect-server-evidence", "aggregate", "destroy-vms")
	return steps
}

func isVideoFeatureProfile(name string) bool {
	switch strings.TrimSpace(name) {
	case VideoCanaryScenarioProfile, Video1KScenarioProfile:
		return true
	default:
		return false
	}
}

func isClipStorageProfile(name string) bool {
	switch strings.TrimSpace(name) {
	case ClipStorageCanaryScenarioProfile, ClipStorage1KScenarioProfile, ClipStorage10KScenarioProfile:
		return true
	default:
		return false
	}
}

func normalizeGateThresholds(opts PlanOptions) (GateThresholds, error) {
	thresholds := GateThresholds{
		FunctionalSuccessThresholdPercent:    defaultFloat(opts.FunctionalSuccessThresholdPercent, DefaultFunctionalSuccessThresholdPercent),
		ClientTargetCompletenessPercent:      defaultFloat(opts.ClientTargetCompletenessPercent, DefaultClientTargetCompletenessPercent),
		ExactEventCorrelationPercent:         defaultFloat(opts.ExactEventCorrelationPercent, DefaultExactEventCorrelationPercent),
		AggregateCorrelationTolerancePercent: defaultFloat(opts.AggregateCorrelationTolerancePercent, DefaultAggregateCorrelationTolerancePercent),
		AggregateCorrelationMinTolerance:     opts.AggregateCorrelationMinTolerance,
	}
	if thresholds.AggregateCorrelationMinTolerance <= 0 {
		thresholds.AggregateCorrelationMinTolerance = DefaultAggregateCorrelationMinTolerance
	}
	for _, item := range []struct {
		name  string
		value float64
	}{
		{"functional success threshold", thresholds.FunctionalSuccessThresholdPercent},
		{"client target completeness threshold", thresholds.ClientTargetCompletenessPercent},
		{"exact event correlation threshold", thresholds.ExactEventCorrelationPercent},
	} {
		if item.value <= 0 || item.value > 100 {
			return GateThresholds{}, fmt.Errorf("%s must be > 0 and <= 100, got %.2f", item.name, item.value)
		}
	}
	if thresholds.AggregateCorrelationTolerancePercent < 0 || thresholds.AggregateCorrelationTolerancePercent > 100 {
		return GateThresholds{}, fmt.Errorf("aggregate correlation tolerance percent must be >= 0 and <= 100, got %.2f", thresholds.AggregateCorrelationTolerancePercent)
	}
	return thresholds, nil
}

func defaultFloat(value float64, fallback float64) float64 {
	if value == 0 {
		return fallback
	}
	return value
}

func gateThresholdsFromConditions(conditions TestConditions) GateThresholds {
	thresholds, err := normalizeGateThresholds(PlanOptions{
		FunctionalSuccessThresholdPercent:    conditions.FunctionalSuccessThresholdPercent,
		ClientTargetCompletenessPercent:      conditions.ClientTargetCompletenessPercent,
		ExactEventCorrelationPercent:         conditions.ExactEventCorrelationPercent,
		AggregateCorrelationTolerancePercent: conditions.AggregateCorrelationTolerancePercent,
		AggregateCorrelationMinTolerance:     conditions.AggregateCorrelationMinTolerance,
	})
	if err != nil {
		return GateThresholds{
			FunctionalSuccessThresholdPercent:    DefaultFunctionalSuccessThresholdPercent,
			ClientTargetCompletenessPercent:      DefaultClientTargetCompletenessPercent,
			ExactEventCorrelationPercent:         DefaultExactEventCorrelationPercent,
			AggregateCorrelationTolerancePercent: DefaultAggregateCorrelationTolerancePercent,
			AggregateCorrelationMinTolerance:     DefaultAggregateCorrelationMinTolerance,
		}
	}
	return thresholds
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

func videoTurnSizingDeviceMixBuckets() []ratioBucket {
	return []ratioBucket{
		{Name: "light", Weight: 15},
		{Name: "switch", Weight: 7},
		{Name: "smart_plug", Weight: 12},
		{Name: "air_conditioner", Weight: 10},
		{Name: "environment_sensor", Weight: 12},
		{Name: "security_sensor", Weight: 10},
		{Name: "smart_meter", Weight: 8},
		{Name: "camera", Weight: 10},
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
	return []Stage{{
		Name:             "target",
		ConnectedDevices: devices,
		WarmUp:           warmUp,
		SteadyState:      steady,
		CoolDown:         coolDown,
	}}
}

func targetWindowFromStages(stages []Stage) TargetWindow {
	if len(stages) == 0 {
		return TargetWindow{}
	}
	target := stages[len(stages)-1]
	return TargetWindow{
		TargetConnects: target.ConnectedDevices,
		RampUpTime:     target.WarmUp,
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

func mixedAssignments(region string, shards []Shard, vmCount int, labelPrefix string) []VMAssignment {
	assignments := make([]VMAssignment, 0, vmCount)
	labelPrefix = strings.TrimSpace(labelPrefix)
	if labelPrefix == "" {
		labelPrefix = DefaultVMLabelPrefix
	}
	for idx := 0; idx < vmCount; idx++ {
		tasks := []Shard{}
		if shard, ok := findShardInList(shards, "device-mqtt", idx); ok {
			tasks = append(tasks, shard)
		}
		if shard, ok := findShardInList(shards, "user-app", idx); ok {
			tasks = append(tasks, shard)
		}
		assignments = append(assignments, VMAssignment{
			Label:      fmt.Sprintf("%s%02d", labelPrefix, idx+1),
			Index:      idx,
			Role:       "mixed",
			Region:     region,
			TaskShards: tasks,
		})
	}
	return assignments
}

func videoAssignments(region string, vmCount int, labelPrefix string) []VMAssignment {
	assignments := make([]VMAssignment, 0, vmCount)
	labelPrefix = strings.TrimSpace(labelPrefix)
	if labelPrefix == "" {
		labelPrefix = "vg"
	}
	for idx := 0; idx < vmCount; idx++ {
		assignments = append(assignments, VMAssignment{
			Label:      fmt.Sprintf("%s%02d", labelPrefix, idx+1),
			Index:      idx,
			Role:       "video",
			Region:     region,
			TaskShards: nil,
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

func (p Plan) AssignmentsByRole(role string) []VMAssignment {
	out := []VMAssignment{}
	for _, assignment := range p.Assignments {
		if assignment.Role == role {
			out = append(out, assignment)
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
	if got := len(p.AssignmentsByRole("mixed")); got != p.Conditions.VMCount {
		return fmt.Errorf("100K mixed baseline requires %d mixed VM assignments, got %d", p.Conditions.VMCount, got)
	}
	if got := len(p.AssignmentsByRole("video")); got != p.Conditions.VideoGeneratorVMCount {
		return fmt.Errorf("video generator assignments = %d, want %d", got, p.Conditions.VideoGeneratorVMCount)
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
