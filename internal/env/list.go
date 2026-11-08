package env

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/wellavelino/province/internal/proxmox"
)

// Live status values besides the API's own (running, stopped).
const (
	LiveUnknown    = "unknown"     // the host could not be asked
	LiveNotCreated = "not_created" // planned only, no guest yet
	LiveMissing    = "missing"     // the recorded vmid is not on the host
)

// View is an environment's record plus what its host reports now.
type View struct {
	Metadata
	LiveStatus string `json:"live_status"`
	Error      string `json:"error,omitempty"`
	SSH        string `json:"ssh,omitempty"`
}

func (m *Manager) view(md *Metadata) View {
	v := View{Metadata: *md}
	if md.Status == StatusBootstrapped {
		v.SSH = "ssh " + m.Cfg.User + "@" + md.Name
	}
	if md.VMID == 0 {
		v.LiveStatus = LiveNotCreated
	}
	return v
}

// List returns every environment in state_dir, sorted by name, with one API
// call per distinct host. An unreachable host marks its environments
// unknown instead of failing the list.
func (m *Manager) List(ctx context.Context) ([]View, error) {
	entries, err := os.ReadDir(m.Cfg.StateDir)
	if errors.Is(err, fs.ErrNotExist) {
		return []View{}, nil
	}
	if err != nil {
		return nil, err
	}
	views := []View{}
	byHost := map[string][]int{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		md, err := readMetadata(filepath.Join(m.Cfg.StateDir, e.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		views = append(views, m.view(md))
		if md.VMID != 0 {
			byHost[md.Host] = append(byHost[md.Host], len(views)-1)
		}
	}
	for hostName, idx := range byHost {
		h, herr := m.Cfg.HostByName(hostName)
		var guests []proxmox.Guest
		var api proxmox.Client
		if herr == nil {
			api, herr = m.Client(h)
		}
		if herr == nil {
			guests, herr = api.ListGuests(ctx, h.Node)
		}
		for _, i := range idx {
			if herr != nil {
				views[i].LiveStatus, views[i].Error = LiveUnknown, herr.Error()
				continue
			}
			views[i].LiveStatus = LiveMissing
			for _, g := range guests {
				if g.VMID == views[i].VMID && g.Type == proxmox.TypeLXC {
					views[i].LiveStatus = g.Status
				}
			}
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}
