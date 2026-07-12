// Package keyvrm implements the "koc keyvrm" command surface for KeyVRM
// (Keystack Virtual Resource Manager), an in-house service registered in the
// Keystone catalog as service type "keyvrm". Auth is the standard Keystone
// token, so it reuses koc's normal auth/output/TLS layers.
//
// This file plus requests.go form the typed request ("SDK") layer, kept separate
// from the cobra/output CLI layer in the other files.
package keyvrm

// AppConfig is the KeyVRM global application configuration (GET/PUT
// /v1/app_config).
type AppConfig struct {
	Enabled                     bool     `json:"enabled"`
	Period                      int      `json:"period"`
	NovaEnabledFilters          string   `json:"nova_enabled_filters"`
	HAPreserveEphemeralDevice   bool     `json:"ha_preserve_ephemeral_device"`
	HAEvacuateOrderKey          string   `json:"ha_evacuate_order_key"`
	HANoEvacuateKey             string   `json:"ha_no_evacuate_key"`
	HAVMStateResetTimeout       int      `json:"ha_vm_state_reset_timeout"`
	HAFenceFailedInterfaces     []string `json:"ha_fence_failed_interfaces"`
	HAFenceCeph                 bool     `json:"ha_fence_ceph"`
	HAFenceBMC                  bool     `json:"ha_fence_bmc"`
	HAFenceNova                 bool     `json:"ha_fence_nova"`
	HACheckFailedInterfaces     []string `json:"ha_check_failed_interfaces"`
	HABondNames                 []string `json:"ha_bond_names"`
	HAPowerFenceMode            string   `json:"ha_power_fence_mode"`
	HAPowerCheckTimeout         int      `json:"ha_power_check_timeout"`
	LBNoMigrateKey              string   `json:"lb_no_migrate_key"`
	ExecutorTimeout             int      `json:"executor_timeout"`
	ExecutorMaxAttempts         int      `json:"executor_max_attempts"`
	ExecutorMaxRepeatedErrors   int      `json:"executor_max_repeated_errors"`
	ExecutorManualActionTimeout int      `json:"executor_manual_action_timeout"`
}

// HostAggregateConfig is a per-host-aggregate KeyVRM configuration.
type HostAggregateConfig struct {
	ID                    string   `json:"id"`
	AvailabilityZoneName  string   `json:"availability_zone_name"`
	HostAggregateName     string   `json:"host_aggregate_name"`
	Marker                *string  `json:"marker"`
	NoOpMode              bool     `json:"no_op_mode"`
	NoOpModeReason        *string  `json:"no_op_mode_reason"`
	HAReservationRatioCPU *float64 `json:"ha_reservation_ratio_cpu"`
	HAReservationRatioRAM *float64 `json:"ha_reservation_ratio_ram"`
	LBCPUWeight           *float64 `json:"lb_cpu_weight"`
	LBRAMWeight           *float64 `json:"lb_ram_weight"`
	LBNetworkWeight       *float64 `json:"lb_network_weight"`
	LBRecommendationsAuto *bool    `json:"lb_recommendations_auto_run"`
	LBThresholdOverload   *int     `json:"lb_threshold_overload"`
	LBThresholdLimit      *int     `json:"lb_threshold_limit"`
	LBPeriod              int      `json:"lb_period"`
	CreatedAt             string   `json:"created_at"`
}

// AvailabilityZone is a KeyVRM availability-zone summary.
type AvailabilityZone struct {
	Name            string `json:"name"`
	AggregatesCount int    `json:"aggregates_count"`
	EventCounts     struct {
		Active  int `json:"active"`
		Warning int `json:"warning"`
		Error   int `json:"error"`
		NoOp    int `json:"noop"`
	} `json:"aggregates_event_counts"`
}

// Event is a host-aggregate event.
type Event struct {
	ID                    string `json:"id"`
	HostAggregateConfigID string `json:"host_aggregate_config_id"`
	Marker                string `json:"marker"`
	Status                string `json:"status"`
	State                 string `json:"state"`
	ErrorDetails          string `json:"error_details"`
	CreatedAt             string `json:"created_at"`
}

// Recommendation is a rebalancing/evacuation recommendation.
type Recommendation struct {
	ID                   string `json:"id"`
	HostAggregateEventID string `json:"host_aggregate_event_id"`
	VMUUID               string `json:"vm_uuid"`
	SourceHVName         string `json:"source_hv_name"`
	DestinationHVName    string `json:"destination_hv_name"`
	Status               string `json:"status"`
	Type                 string `json:"type"`
	Reason               string `json:"reason"`
	EvacuatePriority     *int   `json:"evacuate_priority"`
	CreatedAt            string `json:"created_at"`
}

// Operation is a single executed operation belonging to a recommendation.
type Operation struct {
	ID                 string `json:"id"`
	RecommendationID   string `json:"recommendation_id"`
	Status             string `json:"status"`
	OpenStackRequestID string `json:"openstack_request_id"`
	NovaMigrationID    string `json:"nova_migration_id"`
	ErrorDetails       string `json:"error_details"`
	FailureType        string `json:"failure_type"`
	CreatedAt          string `json:"created_at"`
}

// page is the common paginated envelope KeyVRM returns for list endpoints.
type page[T any] struct {
	Data   []T `json:"data"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// listOpts carries the shared pagination + optional filters for list calls.
type listOpts struct {
	Limit  int
	Offset int
	// filters is a set of resource-specific query params (nil-safe).
	filters map[string]string
}
