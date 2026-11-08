// Package tfgen renders the OpenTofu JSON configuration of an environment.
// No secret ever goes into it: the API token reaches tofu through its
// environment only.
package tfgen

import "encoding/json"

// ProviderVersion pins bpg/proxmox.
const ProviderVersion = "0.120.0"

// ResourceAddress is the container's address in the generated config.
const ResourceAddress = "proxmox_virtual_environment_container.env"

// LXC describes one container environment.
type LXC struct {
	Name     string
	VMID     int // explicit vm_id, free on every host
	Node     string
	Endpoint string
	Insecure bool
	Template string // volume id, e.g. local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst
	Storage  string
	Bridge   string
	CPU      int // cores
	RAM      int // MiB
	Disk     int // GiB
	Keys     []string
	// Docker: keyctl, needed by Docker in an unprivileged container, is set
	// over SSH (an API token may change only nesting), so tofu ignores features.
	Docker bool
	// TunDevice: /dev/net/tun is added over SSH (root@pam only), so tofu
	// ignores device_passthrough.
	TunDevice bool
}

type file struct {
	Terraform terraform           `json:"terraform"`
	Provider  map[string]provider `json:"provider"`
	Resource  resources           `json:"resource"`
	Output    map[string]output   `json:"output"`
}

type terraform struct {
	RequiredProviders map[string]requiredProvider `json:"required_providers"`
}

type requiredProvider struct {
	Source  string `json:"source"`
	Version string `json:"version"`
}

type provider struct {
	Endpoint string `json:"endpoint"`
	Insecure bool   `json:"insecure"`
}

type resources struct {
	Container map[string]container `json:"proxmox_virtual_environment_container"`
}

type container struct {
	NodeName         string             `json:"node_name"`
	VMID             int                `json:"vm_id,omitempty"`
	Description      string             `json:"description"`
	Tags             []string           `json:"tags"`
	Unprivileged     bool               `json:"unprivileged"`
	StartOnBoot      bool               `json:"start_on_boot"`
	Started          bool               `json:"started"`
	Features         features           `json:"features"`
	OperatingSystem  operatingSystem    `json:"operating_system"`
	CPU              cpu                `json:"cpu"`
	Memory           memory             `json:"memory"`
	Disk             disk               `json:"disk"`
	NetworkInterface []networkInterface `json:"network_interface"`
	Initialization   initialization     `json:"initialization"`
	Lifecycle        *lifecycle         `json:"lifecycle,omitempty"`
}

type features struct {
	Nesting bool `json:"nesting"`
}

type operatingSystem struct {
	TemplateFileID string `json:"template_file_id"`
	Type           string `json:"type"`
}

type cpu struct {
	Cores int `json:"cores"`
}

type memory struct {
	Dedicated int `json:"dedicated"`
	Swap      int `json:"swap"`
}

type disk struct {
	DatastoreID string `json:"datastore_id"`
	Size        int    `json:"size"`
}

type networkInterface struct {
	Name   string `json:"name"`
	Bridge string `json:"bridge"`
}

type initialization struct {
	Hostname    string      `json:"hostname"`
	IPConfig    []ipConfig  `json:"ip_config"`
	UserAccount userAccount `json:"user_account"`
}

type ipConfig struct {
	IPv4 ipv4 `json:"ipv4"`
}

type ipv4 struct {
	Address string `json:"address"`
}

type userAccount struct {
	Keys []string `json:"keys"`
}

type lifecycle struct {
	IgnoreChanges []string `json:"ignore_changes"`
}

type output struct {
	Value string `json:"value"`
}

// Generate renders main.tf.json for l.
func Generate(l LXC) ([]byte, error) {
	c := container{
		NodeName:     l.Node,
		VMID:         l.VMID,
		Description:  "Managed by province (env " + l.Name + ")",
		Tags:         []string{"province"},
		Unprivileged: true,
		StartOnBoot:  true,
		Started:      true,
		// nesting: systemd in recent templates needs it (the PVE UI default).
		Features:         features{Nesting: true},
		OperatingSystem:  operatingSystem{TemplateFileID: l.Template, Type: OSType(l.Template)},
		CPU:              cpu{Cores: l.CPU},
		Memory:           memory{Dedicated: l.RAM, Swap: 512},
		Disk:             disk{DatastoreID: l.Storage, Size: l.Disk},
		NetworkInterface: []networkInterface{{Name: "eth0", Bridge: l.Bridge}},
		Initialization: initialization{
			Hostname:    l.Name,
			IPConfig:    []ipConfig{{IPv4: ipv4{Address: "dhcp"}}},
			UserAccount: userAccount{Keys: l.Keys},
		},
	}
	var ignore []string
	if l.Docker {
		ignore = append(ignore, "features")
	}
	if l.TunDevice {
		ignore = append(ignore, "device_passthrough")
	}
	if ignore != nil {
		c.Lifecycle = &lifecycle{IgnoreChanges: ignore}
	}
	f := file{
		Terraform: terraform{RequiredProviders: map[string]requiredProvider{
			"proxmox": {Source: "bpg/proxmox", Version: ProviderVersion},
		}},
		Provider: map[string]provider{"proxmox": {Endpoint: l.Endpoint, Insecure: l.Insecure}},
		Resource: resources{Container: map[string]container{"env": c}},
		Output:   map[string]output{"vmid": {Value: "${" + ResourceAddress + ".vm_id}"}},
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
