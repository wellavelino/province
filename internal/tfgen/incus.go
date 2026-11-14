package tfgen

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
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
	// Isolated attaches a network ACL to eth0 that lets traffic out to
	// anything but the LAN (see IsolatedLAN). Needs Network. Gateways
	// are the network's own addresses (the bridge's ipv4.address and
	// ipv6.address, without prefix length): DNS and DHCP to them stay
	// allowed.
	Isolated bool
	Gateways []string
}

// IncusACLAddress is the isolated env's network ACL in the generated config.
const IncusACLAddress = "incus_network_acl.isolated"

// IsolatedLAN is what an isolated env cannot reach: private, link-local
// IPv4 and unique-local IPv6 ranges. IPv6 link-local (fe80::/10) and
// multicast stay reachable, so neighbour discovery and router
// advertisements keep working.
var IsolatedLAN = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "fc00::/7"}

// TailnetRanges are the tailnet's addresses, allowed explicitly. They lie
// outside IsolatedLAN except fd7a:115c:a1e0::/48, which is inside fc00::/7.
var TailnetRanges = []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}

// isolatedAllowed is every address outside IsolatedLAN, as IP ranges.
// Incus orders ACL rules drop, reject, allow, whatever the list order, so
// a drop rule for the LAN would beat any allow rule for the gateway's DNS.
// The ACL is therefore an allowlist with a default drop: these ranges,
// TailnetRanges and DNS/DHCP to the gateways. TestIsolatedAllowedIsComplement
// checks it against IsolatedLAN.
var isolatedAllowed = []string{
	"0.0.0.0-9.255.255.255",
	"11.0.0.0-169.253.255.255",
	"169.255.0.0-172.15.255.255",
	"172.32.0.0-192.167.255.255",
	"192.169.0.0-255.255.255.255",
	"::-fbff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	"fe00::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
}

type incusFile struct {
	Terraform terraform           `json:"terraform"`
	Provider  map[string]struct{} `json:"provider"`
	Resource  incusResources      `json:"resource"`
	Output    map[string]output   `json:"output"`
}

type incusResources struct {
	Instance map[string]inst       `json:"incus_instance"`
	ACL      map[string]networkACL `json:"incus_network_acl,omitempty"`
}

type networkACL struct {
	Name        lit       `json:"name"`
	Description lit       `json:"description"`
	Remote      lit       `json:"remote"`
	Project     lit       `json:"project,omitempty"`
	Egress      []aclRule `json:"egress"`
}

type aclRule struct {
	Description     lit `json:"description"`
	Action          lit `json:"action"`
	State           lit `json:"state"`
	Protocol        lit `json:"protocol,omitempty"`
	Destination     lit `json:"destination"`
	DestinationPort lit `json:"destination_port,omitempty"`
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
	DependsOn   []expr         `json:"depends_on,omitempty"`
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
	var acls map[string]networkACL
	var dependsOn []expr
	if i.Isolated {
		if i.Network == "" || len(i.Gateways) == 0 {
			return nil, errors.New("tfgen: an isolated env needs a network and its gateway addresses")
		}
		acl := isolatedACL(i)
		acls = map[string]networkACL{"isolated": acl}
		dependsOn = []expr{IncusACLAddress}
		nic := devices[slices.IndexFunc(devices, func(d device) bool { return d.Name == "eth0" })].Properties
		nic["security.acls"] = acl.Name
		nic["security.acls.default.egress.action"] = "drop"
		nic["security.acls.default.ingress.action"] = "allow"
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
		Resource: incusResources{Instance: map[string]inst{"env": {
			Name:        lit(i.Name),
			Description: lit("Managed by province (env " + i.Name + ")"),
			Remote:      lit(i.Remote),
			Project:     lit(i.Project),
			Image:       lit(i.Image),
			Type:        typ,
			Running:     true,
			Config:      cfg,
			Device:      devices,
			DependsOn:   dependsOn,
		}}, ACL: acls},
		Output: map[string]output{"name": {Value: expr("${" + IncusResourceAddress + ".name}")}},
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// IncusACLName is the name of env name's network ACL. Incus ACL names are
// at most 63 characters, so env names longer than 54 cannot be isolated.
func IncusACLName(name string) string { return "province-" + name }

// isolatedACL is the egress allowlist of an isolated env: everything but
// the LAN, the tailnet, and DNS and DHCP to the network's own addresses.
// Ingress is left open (the NIC's default ingress action is allow).
func isolatedACL(i Incus) networkACL {
	gw := lit(strings.Join(i.Gateways, ","))
	return networkACL{
		Name:        lit(IncusACLName(i.Name)),
		Description: lit("Managed by province: env " + i.Name + " reaches the internet and the tailnet, not the LAN"),
		Remote:      lit(i.Remote),
		Project:     lit(i.Project),
		Egress: []aclRule{
			{Description: "internet: everything outside the LAN ranges", Action: "allow", State: "enabled",
				Destination: lit(strings.Join(isolatedAllowed, ","))},
			{Description: "tailnet", Action: "allow", State: "enabled", Destination: lit(strings.Join(TailnetRanges, ","))},
			{Description: "DNS and DHCP to the network's gateway", Action: "allow", State: "enabled", Protocol: "udp",
				Destination: gw, DestinationPort: "53,67,547"},
			{Description: "DNS over TCP to the network's gateway", Action: "allow", State: "enabled", Protocol: "tcp",
				Destination: gw, DestinationPort: "53"},
		},
	}
}
