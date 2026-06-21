package home100k

import "fmt"

type LifecycleAction struct {
	Action      string   `json:"action"`
	RunID       string   `json:"run_id"`
	Role        string   `json:"role,omitempty"`
	ShardIndex  int      `json:"shard_index,omitempty"`
	Region      string   `json:"region,omitempty"`
	Label       string   `json:"label,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description"`
}

func BuildLifecycleActions(plan Plan, runID string) []LifecycleAction {
	actions := []LifecycleAction{}
	for _, assignment := range plan.Assignments {
		actions = append(actions, LifecycleAction{
			Action:      "provision-vm",
			RunID:       runID,
			Role:        assignment.Role,
			ShardIndex:  assignment.Index,
			Region:      assignment.Region,
			Label:       assignment.Label,
			Tags:        []string{"home-100k", runID, "load-generator", assignment.Role},
			Description: fmt.Sprintf("create ephemeral %s load-generator VM for assignment %d", assignment.Role, assignment.Index),
		})
	}
	for _, assignment := range plan.Assignments {
		actions = append(actions, LifecycleAction{
			Action:      "sync",
			RunID:       runID,
			Role:        assignment.Role,
			ShardIndex:  assignment.Index,
			Region:      assignment.Region,
			Label:       assignment.Label,
			Description: "sync required runner code and env-root artifacts only",
		})
	}
	actions = append(actions, LifecycleAction{
		Action:      "run-stages",
		RunID:       runID,
		Description: "run device and user shards through 25K, 50K, 75K, and 100K stages with shared run_id",
	})
	actions = append(actions, LifecycleAction{
		Action:      "collect",
		RunID:       runID,
		Description: "collect per-VM results, local telemetry, and runner logs",
	})
	actions = append(actions, LifecycleAction{
		Action:      "collect-server-evidence",
		RunID:       runID,
		Description: "collect server metrics and logs for each stage window using run_id",
	})
	actions = append(actions, LifecycleAction{
		Action:      "aggregate",
		RunID:       runID,
		Description: "aggregate shard results and server evidence into run-level artifacts and report",
	})
	for _, assignment := range plan.Assignments {
		actions = append(actions, LifecycleAction{
			Action:      "destroy-vm",
			RunID:       runID,
			Role:        assignment.Role,
			ShardIndex:  assignment.Index,
			Region:      assignment.Region,
			Label:       assignment.Label,
			Tags:        []string{"home-100k", runID, "load-generator", assignment.Role},
			Description: "scrub and destroy ephemeral load-generator VM",
		})
	}
	return actions
}
