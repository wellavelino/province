package env

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
	Expired    bool   `json:"expired"`
	Error      string `json:"error,omitempty"`
	SSH        string `json:"ssh,omitempty"`
}

func (m *Manager) view(md *Metadata) View {
	v := View{Metadata: *md, Expired: md.expired(m.Now())}
	if md.Status == StatusBootstrapped {
		v.SSH = "ssh " + m.Cfg.User + "@" + md.Name
	}
	if !md.created() {
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
		if md.created() {
			byHost[md.Host] = append(byHost[md.Host], len(views)-1)
		}
	}
	for _, idx := range byHost {
		mds := make([]*Metadata, len(idx))
		for k, i := range idx {
			mds[k] = &views[i].Metadata
		}
		// hostOf also refuses a host whose backend changed under the env.
		h, err := m.hostOf(mds[0])
		var lv []live
		if err == nil {
			lv, err = m.backendFor(h).statuses(ctx, mds)
		}
		for k, i := range idx {
			if err != nil {
				views[i].LiveStatus, views[i].Error = LiveUnknown, err.Error()
				continue
			}
			views[i].LiveStatus = lv[k].Status
			if lv[k].Err != nil {
				views[i].Error = lv[k].Err.Error()
			}
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}

// recordedVMIDs returns every VMID an env in state_dir records.
func (m *Manager) recordedVMIDs() (map[int]bool, error) {
	ids := map[int]bool{}
	entries, err := os.ReadDir(m.Cfg.StateDir)
	if errors.Is(err, fs.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
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
		if md.VMID != 0 {
			ids[md.VMID] = true
		}
	}
	return ids, nil
}
