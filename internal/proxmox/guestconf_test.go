package proxmox

import (
	"fmt"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{"8G": 8 << 30, "512M": 512 << 20, "4M": 4 << 20, "1T": 1 << 40, "528K": 528 << 10, "1024": 1024, "0.5G": 512 << 20}
	for in, want := range cases {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "G", "abc", "-1G"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func TestDisksLXC(t *testing.T) {
	conf := map[string]string{
		"rootfs":   "local-lvm:vm-105-disk-0,size=8G",
		"mp0":      "data:vm-105-disk-1,mp=/srv,size=32G",
		"mp1":      "/mnt/host,mp=/host",
		"net0":     "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:00:00:01,ip=dhcp,type=veth",
		"dev0":     "/dev/net/tun",
		"hostname": "demo",
	}
	ds, err := Disks(conf)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ds) != "[{mp0 data 34359738368} {rootfs local-lvm 8589934592}]" {
		t.Fatalf("disks = %v", ds)
	}
	if TotalSize(ds) != 40<<30 || PrimaryStorage(ds) != "local-lvm" {
		t.Fatalf("total=%d primary=%q", TotalSize(ds), PrimaryStorage(ds))
	}
	if hb := HostBound(conf); fmt.Sprint(hb) != "[dev0 mp1]" {
		t.Fatalf("host bound = %v", hb)
	}
}

func TestDisksQEMU(t *testing.T) {
	conf := map[string]string{
		"scsi0":    "local-lvm:vm-200-disk-0,iothread=1,size=32G",
		"virtio1":  "ssd:vm-200-disk-1,size=10G",
		"ide2":     "local:iso/debian.iso,media=cdrom,size=600M",
		"ide0":     "none,media=cdrom",
		"efidisk0": "local-lvm:vm-200-disk-2,efitype=4m,size=4M",
		"net0":     "virtio=BC:24:11:00:00:02,bridge=vmbr1,firewall=1",
		"net1":     "virtio=BC:24:11:00:00:03,bridge=vmbr0",
		"hostpci0": "0000:01:00.0",
		"usb0":     "host=1234:5678",
	}
	ds, err := Disks(conf)
	if err != nil {
		t.Fatal(err)
	}
	if TotalSize(ds) != 42<<30+4<<20 || len(ds) != 3 {
		t.Fatalf("disks = %v", ds)
	}
	if PrimaryStorage(ds) != "local-lvm" {
		t.Fatalf("primary = %q", PrimaryStorage(ds))
	}
	if b := NetBridges(conf); fmt.Sprint(b) != "[vmbr0 vmbr1]" {
		t.Fatalf("bridges = %v", b)
	}
	if hb := HostBound(conf); fmt.Sprint(hb) != "[hostpci0 usb0]" {
		t.Fatalf("host bound = %v", hb)
	}
}

func TestDisksRejectsMissingSize(t *testing.T) {
	if _, err := Disks(map[string]string{"rootfs": "local-lvm:vm-105-disk-0"}); err == nil {
		t.Fatal("disk without size accepted: a move would under-estimate space")
	}
}

func TestHostBoundRawLXCKeys(t *testing.T) {
	if hb := HostBound(map[string]string{"lxc.mount.entry": "/dev/dri dev/dri none bind"}); fmt.Sprint(hb) != "[lxc.mount.entry]" {
		t.Fatalf("host bound = %v", hb)
	}
}
