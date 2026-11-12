package tfgen

import (
	"encoding/json"
	"strconv"
)

// IncusProviderVersion pins lxc/incus.
const IncusProviderVersion = "1.2.1"

// IncusResourceAddress is the instance's address in the generated config.
const IncusResourceAddress = "incus_instance.env"

// Incus describes one environment, a container or a VM, on an Incus server. The
// provider authenticates through the operator's Incus client config (the
// remote added with `incus remote add`), so no secret goes in here.
type Incus struct {
	Name    string
	Remote  string
	Project string // empty: the remote's default project
	Image   string // e.g. images:debian/13
	Pool    string // root disk pool
	Network string // empty: keep the profile's eth0
	CPU     int    // cores
	RAM     int    // MiB
	Disk    int    // GiB
	// VM makes the instance a virtual machine (the image's VM variant)
	// instead of a container. A VM has its own kernel, so Docker and
	// TunDevice add nothing to it.
	VM bool
	// Docker turns on nesting and the syscall interception Docker needs in
	// an unprivileged container.
	Docker bool
	// TunDevice passes /dev/net/tun through for tailscale.tun: device.
	TunDevice bool
}

type incusFile struct {
	Terraform terraform                  `json:"terraform"`
	Provider  map[string]struct{}        `json:"provider"`
	Resource  map[string]map[string]inst `json:"resource"`
	Output    map[string]output          `json:"output"`
}

type inst struct {
	Name        lit            `json:"name"`
	Description lit            `json:"description"`
	Remote      lit            `json:"remote"`
	Project     lit            `json:"project,omitempty"`
	Image       lit            `json:"image"`
	Type        lit            `json:"type"`
	Running     bool           `json:"running"`
	Config      map[string]lit `json:"config"`
	Device      []device       `json:"device"`
}

type device struct {
	Name       lit            `json:"name"`
	Type       lit            `json:"type"`
	Properties map[string]lit `json:"properties"`
}

// GenerateIncus renders main.tf.json for i.
func GenerateIncus(i Incus) ([]byte, error) {
	cfg := map[string]lit{
		"boot.autostart":    "true",
		"limits.cpu":        lit(strconv.Itoa(i.CPU)),
		"limits.memory":     lit(strconv.Itoa(i.RAM) + "MiB"),
		"user.province.env": lit(i.Name),
	}
	if i.Docker && !i.VM {
		cfg["security.nesting"] = "true"
		cfg["security.syscalls.intercept.mknod"] = "true"
		cfg["security.syscalls.intercept.setxattr"] = "true"
	}
	devices := []device{{Name: "root", Type: "disk", Properties: map[string]lit{
		"path": "/", "pool": lit(i.Pool), "size": lit(strconv.Itoa(i.Disk) + "GiB"),
	}}}
	if i.Network != "" {
		devices = append(devices, device{Name: "eth0", Type: "nic", Properties: map[string]lit{"name": "eth0", "network": lit(i.Network)}})
	}
	if i.TunDevice && !i.VM {
		devices = append(devices, device{Name: "tun", Type: "unix-char", Properties: map[string]lit{"source": "/dev/net/tun", "path": "/dev/net/tun"}})
	}
	typ := lit("container")
	if i.VM {
		typ = "virtual-machine"
	}
	f := incusFile{
		Terraform: terraform{RequiredProviders: map[string]requiredProvider{
			"incus": {Source: "lxc/incus", Version: lit(IncusProviderVersion)},
		}},
		Provider: map[string]struct{}{"incus": {}},
		Resource: map[string]map[string]inst{"incus_instance": {"env": {
			Name:        lit(i.Name),
			Description: lit("Managed by province (env " + i.Name + ")"),
			Remote:      lit(i.Remote),
			Project:     lit(i.Project),
			Image:       lit(i.Image),
			Type:        typ,
			Running:     true,
			Config:      cfg,
			Device:      devices,
		}}},
		Output: map[string]output{"name": {Value: expr("${" + IncusResourceAddress + ".name}")}},
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
