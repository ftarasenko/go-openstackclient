package quota

import (
	"context"
	"fmt"
	"io"

	volumequotas "github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	computequotas "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	networkquotas "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// quotaSetFlags is one flat flag surface across all three services, matching
// upstream: `openstack quota set --cores 64 --gigabytes 4000 --ports 500` is a
// single command even though it lands on three APIs.
//
// Every value is an int flag read through a "was it given" check, so a quota
// nobody mentioned is never sent — the alternative would reset unrelated quotas
// to zero on every invocation.
type quotaSetFlags struct {
	// compute
	cores              int
	instances          int
	ram                int
	keyPairs           int
	metadataItems      int
	serverGroups       int
	serverGroupMembers int

	// volume
	volumes            int
	snapshots          int
	gigabytes          int
	perVolumeGigabytes int
	backups            int
	backupGigabytes    int
	volumeGroups       int
	force              bool

	// network
	networks           int
	subnets            int
	subnetPools        int
	ports              int
	routers            int
	floatingIPs        int
	securityGroups     int
	securityGroupRules int
	rbacPolicies       int
	trunks             int

	// Resolved in RunE: fl is the command's flag set (a quota of zero is a real
	// value, so "given" cannot be read off the field), and given names the
	// services whose flags were used.
	fl    *pflag.FlagSet
	given serviceSelection
}

// quotaFlag binds one CLI flag name to the int it populates.
type quotaFlag struct {
	name string
	dest *int
}

func (f *quotaSetFlags) computeFlags() []quotaFlag {
	return []quotaFlag{
		{flagCores, &f.cores},
		{flagInstances, &f.instances},
		{flagRAM, &f.ram},
		{flagKeyPairs, &f.keyPairs},
		{flagProperties, &f.metadataItems},
		{flagServerGroups, &f.serverGroups},
		{flagServerGroupMembers, &f.serverGroupMembers},
	}
}

func (f *quotaSetFlags) volumeFlags() []quotaFlag {
	return []quotaFlag{
		{flagVolumes, &f.volumes},
		{flagSnapshots, &f.snapshots},
		{flagGigabytes, &f.gigabytes},
		{flagPerVolumeGigabytes, &f.perVolumeGigabytes},
		{flagBackups, &f.backups},
		{flagBackupGigabytes, &f.backupGigabytes},
		{flagVolumeGroups, &f.volumeGroups},
	}
}

func (f *quotaSetFlags) networkFlags() []quotaFlag {
	return []quotaFlag{
		{flagNetworks, &f.networks},
		{flagSubnets, &f.subnets},
		{flagSubnetPools, &f.subnetPools},
		{flagPorts, &f.ports},
		{flagRouters, &f.routers},
		{flagFloatingIPs, &f.floatingIPs},
		{flagSecgroups, &f.securityGroups},
		{flagSecgroupRules, &f.securityGroupRules},
		{flagRBACPolicies, &f.rbacPolicies},
		{flagTrunks, &f.trunks},
	}
}

func newQuotaSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &quotaSetFlags{}
	cmd := &cobra.Command{
		Use:   "set [<project>]",
		Short: "Set a project's compute, volume and/or network quotas",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.fl = cmd.Flags()
			f.given = f.givenBy(f.fl)
			if !f.given.compute && !f.given.volume && !f.given.network {
				return fmt.Errorf("nothing to set: pass at least one quota flag (see \"koc quota set --help\")")
			}
			ctx := cmd.Context()
			s, err := newSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := s.resolveProject(ctx, a, args)
			if err != nil {
				return err
			}
			return runQuotaSet(ctx, s, o, project, f, cmd.OutOrStdout())
		},
	}

	fl := cmd.Flags()
	descriptions := map[string]string{
		flagCores:              "compute: total number of cores",
		flagInstances:          "compute: number of instances",
		flagRAM:                "compute: megabytes of instance RAM",
		flagKeyPairs:           "compute: number of key pairs",
		flagProperties:         "compute: metadata items allowed per instance",
		flagServerGroups:       "compute: number of server groups",
		flagServerGroupMembers: "compute: members allowed per server group",
		flagVolumes:            "volume: number of volumes",
		flagSnapshots:          "volume: number of snapshots",
		flagGigabytes:          "volume: total gigabytes of volumes and snapshots",
		flagPerVolumeGigabytes: "volume: gigabytes allowed for a single volume",
		flagBackups:            "volume: number of backups",
		flagBackupGigabytes:    "volume: total gigabytes of backups",
		flagVolumeGroups:       "volume: number of volume groups",
		flagNetworks:           "network: number of networks",
		flagSubnets:            "network: number of subnets",
		flagSubnetPools:        "network: number of subnet pools",
		flagPorts:              "network: number of ports",
		flagRouters:            "network: number of routers",
		flagFloatingIPs:        "network: number of floating IPs",
		flagSecgroups:          "network: number of security groups",
		flagSecgroupRules:      "network: number of security group rules",
		flagRBACPolicies:       "network: number of RBAC policies",
		flagTrunks:             "network: number of network trunks",
	}
	for _, group := range [][]quotaFlag{f.computeFlags(), f.volumeFlags(), f.networkFlags()} {
		for _, qf := range group {
			fl.IntVar(qf.dest, qf.name, 0, descriptions[qf.name])
		}
	}
	// --force is cinder-only: it lets a quota drop below current usage.
	fl.BoolVar(&f.force, "force", false, "volume: apply the quota even if it is below current usage")
	return cmd
}

// givenBy reports which services have at least one flag set on this invocation,
// so only those APIs are called.
func (f *quotaSetFlags) givenBy(fl *pflag.FlagSet) serviceSelection {
	anyGiven := func(group []quotaFlag) bool {
		for _, qf := range group {
			if fl.Changed(qf.name) {
				return true
			}
		}
		return false
	}
	return serviceSelection{
		compute: anyGiven(f.computeFlags()),
		volume:  anyGiven(f.volumeFlags()) || fl.Changed("force"),
		network: anyGiven(f.networkFlags()),
	}
}

// runQuotaSet updates each service whose flags were given, then reports the new
// values for exactly those services. It deliberately does not roll back a
// partial success: the three APIs have no shared transaction, so an error after
// the first update is reported with the services already changed named in it.
func runQuotaSet(ctx context.Context, s *session, o *output.Options, project string,
	f *quotaSetFlags, w io.Writer,
) error {
	fl, given := f.fl, f.given
	// ptr yields a pointer to the flag's value only when it was actually given,
	// which is how every quota UpdateOpts distinguishes "set to N" from
	// "leave alone" (all fields are *int with omitempty).
	ptr := func(name string, v *int) *int {
		if !fl.Changed(name) {
			return nil
		}
		n := *v
		return &n
	}

	var fields []string
	var values []any
	var applied []string

	if given.compute {
		client, err := s.compute()
		if err != nil {
			return err
		}
		opts := computequotas.UpdateOpts{
			Cores:              ptr(flagCores, &f.cores),
			Instances:          ptr(flagInstances, &f.instances),
			RAM:                ptr(flagRAM, &f.ram),
			KeyPairs:           ptr(flagKeyPairs, &f.keyPairs),
			MetadataItems:      ptr(flagProperties, &f.metadataItems),
			ServerGroups:       ptr(flagServerGroups, &f.serverGroups),
			ServerGroupMembers: ptr(flagServerGroupMembers, &f.serverGroupMembers),
		}
		qs, err := computequotas.Update(ctx, client, project, opts).Extract()
		if err != nil {
			return fmt.Errorf("setting compute quotas for project %q: %w", project, err)
		}
		cf, cv := computeQuotaFields(qs)
		fields, values = append(fields, cf...), append(values, cv...)
		applied = append(applied, "compute")
	}

	if given.volume {
		client, err := s.volume()
		if err != nil {
			return partialError(applied, "volume", project, err)
		}
		opts := volumequotas.UpdateOpts{
			Volumes:            ptr(flagVolumes, &f.volumes),
			Snapshots:          ptr(flagSnapshots, &f.snapshots),
			Gigabytes:          ptr(flagGigabytes, &f.gigabytes),
			PerVolumeGigabytes: ptr(flagPerVolumeGigabytes, &f.perVolumeGigabytes),
			Backups:            ptr(flagBackups, &f.backups),
			BackupGigabytes:    ptr(flagBackupGigabytes, &f.backupGigabytes),
			Groups:             ptr(flagVolumeGroups, &f.volumeGroups),
			Force:              f.force,
		}
		qs, err := volumequotas.Update(ctx, client, project, opts).Extract()
		if err != nil {
			return partialError(applied, "volume", project, err)
		}
		vf, vv := volumeQuotaFields(qs)
		fields, values = append(fields, vf...), append(values, vv...)
		applied = append(applied, "volume")
	}

	if given.network {
		client, err := s.network()
		if err != nil {
			return partialError(applied, "network", project, err)
		}
		opts := networkquotas.UpdateOpts{
			Network:           ptr(flagNetworks, &f.networks),
			Subnet:            ptr(flagSubnets, &f.subnets),
			SubnetPool:        ptr(flagSubnetPools, &f.subnetPools),
			Port:              ptr(flagPorts, &f.ports),
			Router:            ptr(flagRouters, &f.routers),
			FloatingIP:        ptr(flagFloatingIPs, &f.floatingIPs),
			SecurityGroup:     ptr(flagSecgroups, &f.securityGroups),
			SecurityGroupRule: ptr(flagSecgroupRules, &f.securityGroupRules),
			RBACPolicy:        ptr(flagRBACPolicies, &f.rbacPolicies),
			Trunk:             ptr(flagTrunks, &f.trunks),
		}
		q, err := networkquotas.Update(ctx, client, project, opts).Extract()
		if err != nil {
			return partialError(applied, "network", project, err)
		}
		nf, nv := networkQuotaFields(q)
		fields, values = append(fields, nf...), append(values, nv...)
	}

	return o.WriteSingle(w, fields, values)
}

// partialError names the services already updated when a later one fails, since
// the three quota APIs share no transaction and cannot be rolled back together.
func partialError(applied []string, failed, project string, err error) error {
	if len(applied) == 0 {
		return fmt.Errorf("setting %s quotas for project %q: %w", failed, project, err)
	}
	return fmt.Errorf("setting %s quotas for project %q: %w (the %v quotas were already updated and are unchanged by this failure)",
		failed, project, err, applied)
}
