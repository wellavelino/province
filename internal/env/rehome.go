package env

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/secrets"
	"github.com/wellavelino/province/internal/tfgen"
)

// FindByGuest returns the name of the env recorded with vmid on host, or "".
func (m *Manager) FindByGuest(host string, vmid int) (string, error) {
	entries, err := os.ReadDir(m.Cfg.StateDir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
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
			return "", err
		}
		if md.Host == host && md.VMID == vmid && md.VMID != 0 {
			return md.Name, nil
		}
	}
	return "", nil
}

// RehomeResult says whether an env's records follow its moved guest.
type RehomeResult struct {
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	VMID          int      `json:"vmid"`
	TofuOutOfSync bool     `json:"tofu_out_of_sync"`
	Fix           []string `json:"fix,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// Rehome points env name at its guest after a move to host `to` as vmid:
// metadata, main.tf.json, then the tofu state (state rm + import with the
// target host's token). The guest itself is never touched. If a step
// fails, the env is marked tofu_out_of_sync with the remaining steps, and
// destroy refuses until they are done.
func (m *Manager) Rehome(ctx context.Context, name string, to config.Host, vmid int, storage string) *RehomeResult {
	res := &RehomeResult{Name: name, Host: to.Name, VMID: vmid, TofuOutOfSync: true}
	dir := m.dir(name)
	md, err := readMetadata(dir)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	importID := to.Node + "/" + strconv.Itoa(vmid)
	fix := func(stateRemoved bool) []string {
		steps := []string{"cd " + dir}
		if to.TokenEnv != secrets.EnvAPIToken {
			steps = append(steps, fmt.Sprintf(`export %s="$%s"`, secrets.EnvAPIToken, to.TokenEnv))
		}
		steps = append(steps, "tofu init -input=false")
		if !stateRemoved {
			steps = append(steps, "tofu state rm "+tfgen.ResourceAddress)
		}
		return append(steps,
			"tofu import "+tfgen.ResourceAddress+" "+importID,
			fmt.Sprintf(`then remove "tofu_out_of_sync" and "tofu_fix" from %s`, filepath.Join(dir, MetadataFile)))
	}
	fail := func(stateRemoved bool, err error) *RehomeResult {
		res.Fix, res.Error = fix(stateRemoved), err.Error()
		md.TofuFix = res.Fix
		if werr := m.writeMetadata(dir, md); werr != nil {
			res.Error += "; " + werr.Error()
		}
		m.logf("warning: env %s moved, but its tofu state still points at the old guest: %s; finish by hand:\n  %s",
			name, res.Error, strings.Join(res.Fix, "\n  "))
		return res
	}

	md.Host, md.Node, md.VMID = to.Name, to.Node, vmid
	if storage != "" {
		md.Storage = storage
	}
	md.TofuOutOfSync, md.TofuFix = true, fix(false)
	if err := m.writeMetadata(dir, md); err != nil {
		return fail(false, err)
	}
	keys, err := m.readKeys()
	if err != nil {
		return fail(false, err)
	}
	if err := m.writeMainTF(dir, md, to, planned{VMID: vmid}, keys); err != nil {
		return fail(false, err)
	}
	tf, err := m.Tofu(to)
	if err != nil {
		return fail(false, err)
	}
	if err := tf.Init(ctx, dir); err != nil {
		return fail(false, err)
	}
	if err := tf.StateRm(ctx, dir, tfgen.ResourceAddress); err != nil {
		return fail(false, err)
	}
	if err := tf.Import(ctx, dir, tfgen.ResourceAddress, importID); err != nil {
		return fail(true, err)
	}
	md.TofuOutOfSync, md.TofuFix = false, nil
	if err := m.writeMetadata(dir, md); err != nil {
		res.Error = err.Error()
		return res
	}
	res.TofuOutOfSync = false
	return res
}
