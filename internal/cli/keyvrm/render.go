package keyvrm

// deref* helpers render nullable KeyVRM fields as display values ("" for nil).

func derefStr(p *string) any {
	if p == nil {
		return ""
	}
	return *p
}

func derefFloat(p *float64) any {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) any {
	if p == nil {
		return ""
	}
	return *p
}

func derefBool(p *bool) any {
	if p == nil {
		return ""
	}
	return *p
}

// appConfigView flattens an AppConfig into aligned field/value slices for
// WriteSingle, covering the full configuration.
func appConfigView(c *AppConfig) ([]string, []any) {
	fields := []string{
		"enabled", "period", "nova_enabled_filters",
		"ha_preserve_ephemeral_device", "ha_evacuate_order_key", "ha_no_evacuate_key",
		"ha_vm_state_reset_timeout", "ha_fence_failed_interfaces", "ha_fence_ceph",
		"ha_fence_bmc", "ha_fence_nova", "ha_check_failed_interfaces", "ha_bond_names",
		"ha_power_fence_mode", "ha_power_check_timeout", "lb_no_migrate_key",
		"executor_timeout", "executor_max_attempts", "executor_max_repeated_errors",
		"executor_manual_action_timeout",
	}
	values := []any{
		c.Enabled, c.Period, c.NovaEnabledFilters,
		c.HAPreserveEphemeralDevice, c.HAEvacuateOrderKey, c.HANoEvacuateKey,
		c.HAVMStateResetTimeout, c.HAFenceFailedInterfaces, c.HAFenceCeph,
		c.HAFenceBMC, c.HAFenceNova, c.HACheckFailedInterfaces, c.HABondNames,
		c.HAPowerFenceMode, c.HAPowerCheckTimeout, c.LBNoMigrateKey,
		c.ExecutorTimeout, c.ExecutorMaxAttempts, c.ExecutorMaxRepeatedErrors,
		c.ExecutorManualActionTimeout,
	}
	return fields, values
}

var haConfigColumns = []string{
	"ID", "Availability Zone", "Host Aggregate", "Marker", "No-Op", "LB Period", "Created At",
}

func haConfigRow(h HostAggregateConfig) []any {
	return []any{
		h.ID, h.AvailabilityZoneName, h.HostAggregateName, derefStr(h.Marker),
		h.NoOpMode, h.LBPeriod, h.CreatedAt,
	}
}

func haConfigView(h *HostAggregateConfig) ([]string, []any) {
	fields := []string{
		"id", "availability_zone_name", "host_aggregate_name", "marker",
		"no_op_mode", "no_op_mode_reason", "ha_reservation_ratio_cpu",
		"ha_reservation_ratio_ram", "lb_cpu_weight", "lb_ram_weight",
		"lb_network_weight", "lb_recommendations_auto_run", "lb_threshold_overload",
		"lb_threshold_limit", "lb_period", "created_at",
	}
	values := []any{
		h.ID, h.AvailabilityZoneName, h.HostAggregateName, derefStr(h.Marker),
		h.NoOpMode, derefStr(h.NoOpModeReason), derefFloat(h.HAReservationRatioCPU),
		derefFloat(h.HAReservationRatioRAM), derefFloat(h.LBCPUWeight), derefFloat(h.LBRAMWeight),
		derefFloat(h.LBNetworkWeight), derefBool(h.LBRecommendationsAuto), derefInt(h.LBThresholdOverload),
		derefInt(h.LBThresholdLimit), h.LBPeriod, h.CreatedAt,
	}
	return fields, values
}

var azColumns = []string{"Name", "Aggregates", "Active", "Warning", "Error", "No-Op"}

func azRow(z AvailabilityZone) []any {
	return []any{z.Name, z.AggregatesCount, z.EventCounts.Active, z.EventCounts.Warning, z.EventCounts.Error, z.EventCounts.NoOp}
}

var eventColumns = []string{"ID", "Host Aggregate Config", "Marker", "Status", "State", "Created At"}

func eventRow(e Event) []any {
	return []any{e.ID, e.HostAggregateConfigID, e.Marker, e.Status, e.State, e.CreatedAt}
}

func eventView(e *Event) ([]string, []any) {
	fields := []string{"id", "host_aggregate_config_id", "marker", "status", "state", "error_details", "created_at"}
	values := []any{e.ID, e.HostAggregateConfigID, e.Marker, e.Status, e.State, e.ErrorDetails, e.CreatedAt}
	return fields, values
}

var recColumns = []string{"ID", "Event", "VM", "Source", "Destination", "Status", "Type", "Priority"}

func recRow(r Recommendation) []any {
	return []any{r.ID, r.HostAggregateEventID, r.VMUUID, r.SourceHVName, r.DestinationHVName, r.Status, r.Type, derefInt(r.EvacuatePriority)}
}

func recView(r *Recommendation) ([]string, []any) {
	fields := []string{
		"id", "host_aggregate_event_id", "vm_uuid", "source_hv_name",
		"destination_hv_name", "status", "type", "reason", "evacuate_priority", "created_at",
	}
	values := []any{
		r.ID, r.HostAggregateEventID, r.VMUUID, r.SourceHVName,
		r.DestinationHVName, r.Status, r.Type, r.Reason, derefInt(r.EvacuatePriority), r.CreatedAt,
	}
	return fields, values
}

var opColumns = []string{"ID", "Recommendation", "Status", "Nova Migration", "Failure Type", "Created At"}

func opRow(o Operation) []any {
	return []any{o.ID, o.RecommendationID, o.Status, o.NovaMigrationID, o.FailureType, o.CreatedAt}
}
