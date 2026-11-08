package proxmox

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ParseSize parses a PVE size such as 8G, 512M, 4M, 1T or 1024 (bytes).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'K', 'k':
			mult = 1 << 10
		case 'M', 'm':
			mult = 1 << 20
		case 'G', 'g':
			mult = 1 << 30
		case 'T', 't':
			mult = 1 << 40
		}
		if mult > 1 {
			s = s[:n-1]
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(f * float64(mult)), nil
}

var (
	diskKeyRE        = regexp.MustCompile(`^(rootfs|mp\d+|scsi\d+|virtio\d+|sata\d+|ide\d+|efidisk\d+|tpmstate\d+)$`)
	netKeyRE         = regexp.MustCompile(`^net\d+$`)
	passthroughKeyRE = regexp.MustCompile(`^(dev\d+|usb\d+|hostpci\d+)$`)
	mpKeyRE          = regexp.MustCompile(`^mp\d+$`)
)

// Disk is one volume of a guest config.
type Disk struct {
	Key     string
	Storage string
	Size    int64
}

// options splits "vol,k=v,k=v" into the volume and its options.
func options(v string) (string, map[string]string) {
	parts := strings.Split(v, ",")
	opts := map[string]string{}
	vol := ""
	for i, p := range parts {
		k, val, ok := strings.Cut(p, "=")
		if !ok {
			if i == 0 {
				vol = p
			}
			continue
		}
		opts[k] = val
	}
	return vol, opts
}

// Disks lists the guest's storage-backed volumes, sorted by key. CD-ROMs,
// "none" drives and bind mounts (no storage, no size) are skipped.
func Disks(conf map[string]string) ([]Disk, error) {
	var out []Disk
	for k, v := range conf {
		if !diskKeyRE.MatchString(k) {
			continue
		}
		vol, opts := options(v)
		if opts["media"] == "cdrom" || vol == "none" || strings.HasPrefix(vol, "/") {
			continue
		}
		if vol == "" {
			// "file=..." form
			vol = opts["file"]
		}
		storage, _, ok := strings.Cut(vol, ":")
		if !ok {
			continue
		}
		sz, ok := opts["size"]
		if !ok {
			return nil, fmt.Errorf("%s: no size in %q", k, v)
		}
		n, err := ParseSize(sz)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out = append(out, Disk{Key: k, Storage: storage, Size: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// TotalSize sums the disk sizes.
func TotalSize(disks []Disk) int64 {
	var n int64
	for _, d := range disks {
		n += d.Size
	}
	return n
}

// PrimaryStorage is the rootfs storage of a container, else the first disk's.
func PrimaryStorage(disks []Disk) string {
	for _, d := range disks {
		if d.Key == "rootfs" {
			return d.Storage
		}
	}
	if len(disks) > 0 {
		return disks[0].Storage
	}
	return ""
}

// NetBridges lists the bridges the guest's NICs attach to, sorted, unique.
func NetBridges(conf map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for k, v := range conf {
		if !netKeyRE.MatchString(k) {
			continue
		}
		_, opts := options(v)
		if b := opts["bridge"]; b != "" && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	sort.Strings(out)
	return out
}

// HostBound lists config keys that tie the guest to its current host and
// will not carry over cleanly: device and PCI/USB passthrough, raw lxc.*
// keys and bind mounts of host paths. Sorted.
func HostBound(conf map[string]string) []string {
	var out []string
	for k, v := range conf {
		switch {
		case passthroughKeyRE.MatchString(k), strings.HasPrefix(k, "lxc."):
			out = append(out, k)
		case mpKeyRE.MatchString(k):
			if vol, _ := options(v); strings.HasPrefix(vol, "/") {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
