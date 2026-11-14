// Command cbt-probe is part of spike MIG-020. It records what the vSphere API simulator
// (vcsim, shipped with govmomi) does with the calls the warm read path needs: switch
// change tracking on, take a snapshot without memory and without quiescing, read the
// change identifier of each disk, ask for changed areas (QueryChangedDiskAreas) with "*"
// and with a stale identifier, switch change tracking off and on, remove the snapshot,
// and check the server certificate by its SHA-1 thumbprint at login.
//
// It prints one "RESULT key=value" line per fact. It never exits non-zero for a refused
// call: a refusal is a measured value.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

func result(key, format string, args ...any) {
	fmt.Printf("RESULT %s=%s\n", key, fmt.Sprintf(format, args...))
}

// describe names the fault type behind an error, or "ok".
func describe(err error) string {
	if err == nil {
		return "ok"
	}
	kind := fmt.Sprintf("%T", err)
	if soap.IsSoapFault(err) {
		if f := soap.ToSoapFault(err).Detail.Fault; f != nil {
			kind = fmt.Sprintf("%T", f)
		}
	} else if soap.IsVimFault(err) {
		kind = fmt.Sprintf("%T", soap.ToVimFault(err))
	}
	return fmt.Sprintf("fault=%s message=%q", kind, strings.ReplaceAll(err.Error(), "\n", " "))
}

type diskInfo struct {
	key      int32
	capacity int64
	backing  string
	changeID string
}

func disks(devices []types.BaseVirtualDevice) []diskInfo {
	var out []diskInfo
	for _, d := range devices {
		disk, ok := d.(*types.VirtualDisk)
		if !ok {
			continue
		}
		info := diskInfo{key: disk.Key, capacity: disk.CapacityInBytes, backing: fmt.Sprintf("%T", disk.Backing)}
		switch b := disk.Backing.(type) {
		case *types.VirtualDiskFlatVer2BackingInfo:
			info.changeID = b.ChangeId
		case *types.VirtualDiskSparseVer2BackingInfo:
			info.changeID = b.ChangeId
		}
		out = append(out, info)
	}
	return out
}

func vmState(ctx context.Context, vm *object.VirtualMachine, label string) []diskInfo {
	var m mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"config", "capability", "runtime"}, &m); err != nil {
		result(label+".properties", "%s", describe(err))
		return nil
	}
	enabled := "unset"
	if m.Config != nil && m.Config.ChangeTrackingEnabled != nil {
		enabled = fmt.Sprintf("%t", *m.Config.ChangeTrackingEnabled)
	}
	supported := fmt.Sprintf("%t", m.Capability.ChangeTrackingSupported)
	result(label+".change_tracking_enabled", "%s", enabled)
	result(label+".change_tracking_supported", "%s", supported)
	result(label+".power_state", "%s", m.Runtime.PowerState)
	var ds []diskInfo
	if m.Config != nil {
		ds = disks(m.Config.Hardware.Device)
	}
	for _, d := range ds {
		result(fmt.Sprintf("%s.disk_%d", label, d.key), "capacity=%d backing=%s change_id=%q", d.capacity, d.backing, d.changeID)
	}
	return ds
}

func snapshotDisks(ctx context.Context, vm *object.VirtualMachine, ref types.ManagedObjectReference, label string) []diskInfo {
	var s mo.VirtualMachineSnapshot
	if err := vm.Properties(ctx, ref, []string{"config.hardware"}, &s); err != nil {
		result(label+".properties", "%s", describe(err))
		return nil
	}
	ds := disks(s.Config.Hardware.Device)
	for _, d := range ds {
		result(fmt.Sprintf("%s.disk_%d", label, d.key), "capacity=%d change_id=%q", d.capacity, d.changeID)
	}
	return ds
}

func snapshotNames(ctx context.Context, vm *object.VirtualMachine) []string {
	var m mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"snapshot"}, &m); err != nil || m.Snapshot == nil {
		return nil
	}
	var names []string
	var walk func([]types.VirtualMachineSnapshotTree)
	walk = func(ts []types.VirtualMachineSnapshotTree) {
		for _, t := range ts {
			names = append(names, t.Name)
			walk(t.ChildSnapshotList)
		}
	}
	walk(m.Snapshot.RootSnapshotList)
	return names
}

func setChangeTracking(ctx context.Context, vm *object.VirtualMachine, on bool) error {
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{ChangeTrackingEnabled: types.NewBool(on)})
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func createSnapshot(ctx context.Context, vm *object.VirtualMachine, name string) (*types.ManagedObjectReference, error) {
	task, err := vm.CreateSnapshot(ctx, name, "spike MIG-020", false, false)
	if err != nil {
		return nil, err
	}
	info, err := task.WaitForResult(ctx)
	if err != nil {
		return nil, err
	}
	ref, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		return nil, fmt.Errorf("snapshot task returned %T", info.Result)
	}
	return &ref, nil
}

func queryAreas(ctx context.Context, c *vim25.Client, vm *object.VirtualMachine, snap *types.ManagedObjectReference, key int32, changeID, label string) {
	req := types.QueryChangedDiskAreas{This: vm.Reference(), Snapshot: snap, DeviceKey: key, StartOffset: 0, ChangeId: changeID}
	res, err := methods.QueryChangedDiskAreas(ctx, c, &req)
	if err != nil {
		result(label, "%s", describe(err))
		return
	}
	result(label, "ok start=%d length=%d areas=%d", res.Returnval.StartOffset, res.Returnval.Length, len(res.Returnval.ChangedArea))
}

func probeThumbprint(ctx context.Context, s *simulator.Server) {
	right := soap.ThumbprintSHA1(s.Certificate())
	result("thumbprint.format", "%d bytes, colon separated", len(strings.Split(right, ":")))
	try := func(label, tp string) {
		u := *s.URL
		sc := soap.NewClient(&u, false)
		if tp != "" {
			sc.SetThumbprint(u.Host, tp)
		}
		vc, err := vim25.NewClient(ctx, sc)
		if err == nil {
			err = session.NewManager(vc).Login(ctx, u.User)
		}
		result("thumbprint."+label, "%s", describe(err))
	}
	try("right", right)
	try("wrong", "00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33")
	try("none", "")
}

func probeChangeTracking(ctx context.Context, s *simulator.Server) {
	c, err := govmomi.NewClient(ctx, s.URL, true)
	if err != nil {
		result("cbt.login", "%s", describe(err))
		return
	}
	finder := find.NewFinder(c.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		result("cbt.datacenter", "%s", describe(err))
		return
	}
	finder.SetDatacenter(dc)
	vms, err := finder.VirtualMachineList(ctx, "*")
	if err != nil || len(vms) == 0 {
		result("cbt.vm_list", "%s count=%d", describe(err), len(vms))
		return
	}
	vm := vms[0]
	result("cbt.vm", "%s", vm.Name())

	// A snapshot that exists before change tracking is switched on (test 6 of the spike).
	pre, err := createSnapshot(ctx, vm, "openova-mig020-pre")
	result("cbt.snapshot_before_tracking", "%s", describe(err))

	before := vmState(ctx, vm, "cbt.before")
	result("cbt.enable", "%s", describe(setChangeTracking(ctx, vm, true)))
	after := vmState(ctx, vm, "cbt.after_enable")
	if len(after) == 0 {
		after = before
	}

	snap1, err := createSnapshot(ctx, vm, "openova-mig020-c1")
	result("cbt.snapshot_c1", "%s", describe(err))
	if snap1 != nil && len(after) > 0 {
		s1 := snapshotDisks(ctx, vm, *snap1, "cbt.snapshot_c1")
		key := after[0].key
		queryAreas(ctx, c.Client, vm, snap1, key, "*", "cbt.query_all_allocated")
		id := ""
		if len(s1) > 0 {
			id = s1[0].changeID
		}
		result("cbt.snapshot_c1.first_disk_change_id_present", "%t", id != "")
		queryAreas(ctx, c.Client, vm, snap1, key, "52 de 3a 11 00 00 00 00-00 00 00 00 00 00 00 01/7", "cbt.query_stale_id")
		if id != "" {
			queryAreas(ctx, c.Client, vm, snap1, key, id, "cbt.query_since_c1")
		}
	}
	if pre != nil && len(after) > 0 {
		queryAreas(ctx, c.Client, vm, pre, after[0].key, "*", "cbt.query_on_pre_tracking_snapshot")
	}

	// Switch tracking off and on between cycles (test 4 of the spike).
	result("cbt.disable", "%s", describe(setChangeTracking(ctx, vm, false)))
	vmState(ctx, vm, "cbt.after_disable")
	result("cbt.reenable", "%s", describe(setChangeTracking(ctx, vm, true)))
	vmState(ctx, vm, "cbt.after_reenable")

	for _, name := range []string{"openova-mig020-c1", "openova-mig020-pre"} {
		task, err := vm.RemoveSnapshot(ctx, name, false, types.NewBool(true))
		if err == nil {
			err = task.Wait(ctx)
		}
		result("cbt.remove_"+strings.TrimPrefix(name, "openova-mig020-"), "%s", describe(err))
	}
	left := 0
	for _, n := range snapshotNames(ctx, vm) {
		if strings.HasPrefix(n, "openova-") {
			left++
		}
	}
	result("cbt.snapshots_left_with_openova_prefix", "%d", left)
}

func main() {
	if bi, ok := debug.ReadBuildInfo(); ok {
		result("go", "%s", bi.GoVersion)
		for _, d := range bi.Deps {
			if d.Path == "github.com/vmware/govmomi" {
				result("govmomi", "%s", d.Version)
			}
		}
	}
	ctx := context.Background()
	model := simulator.VPX()
	defer model.Remove()
	if err := model.Create(); err != nil {
		result("simulator", "%s", describe(err))
		return
	}
	model.Service.TLS = new(tls.Config)
	s := model.Service.NewServer()
	defer s.Close()
	result("simulator", "ok url_scheme=%s", s.URL.Scheme)
	probeThumbprint(ctx, s)
	probeChangeTracking(ctx, s)
}
