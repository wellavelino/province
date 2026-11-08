package proxmox

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Guest types as the API names them.
const (
	TypeLXC  = "lxc"
	TypeQEMU = "qemu"
)

// Guest is one container or VM on a node.
type Guest struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Type   string `json:"type"` // lxc | qemu
}

// Storage is one storage as seen from a node.
type Storage struct {
	Name    string
	Type    string   // dir, lvmthin, zfspool, nfs, ...
	Content []string // e.g. rootdir, images, vztmpl, backup
	Avail   int64    // bytes
	Active  bool
}

// Has reports whether the storage accepts the content type.
func (s Storage) Has(content string) bool { return slices.Contains(s.Content, content) }

// flexInt decodes a JSON number or numeric string (the API is not consistent).
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", b)
	}
	*f = flexInt(n)
	return nil
}

type apiGuest struct {
	VMID   flexInt `json:"vmid"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
}

type apiStorage struct {
	Storage string  `json:"storage"`
	Content string  `json:"content"`
	Avail   flexInt `json:"avail"`
	Active  flexInt `json:"active"`
	Type    string  `json:"type"`
}

type apiIface struct {
	Iface string `json:"iface"`
}

type apiVolume struct {
	VolID string `json:"volid"`
}

// configValues turns a guest config's JSON values into strings. Raw lxc.*
// keys arrive as "lxc": [[key, value], ...] and are expanded.
func configValues(raw map[string]json.RawMessage) map[string]string {
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "lxc" {
			var pairs [][]string
			if json.Unmarshal(v, &pairs) == nil {
				for _, p := range pairs {
					if len(p) == 2 {
						out[p[0]] = p[1]
					}
				}
				continue
			}
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		out[k] = string(v)
	}
	return out
}
