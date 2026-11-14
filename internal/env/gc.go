package env

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wellavelino/province/internal/errs"
)

// lifetime records create's --ttl and --protect on md. The expiry is set
// when --apply first runs (a planned env has no guest to collect), or
// again when --apply is given a new --ttl; it is rounded up to the second.
func (md *Metadata) lifetime(o CreateOpts, now time.Time) {
	if o.Protect {
		md.Protected = true
	}
	if o.TTL > 0 {
		md.TTL = o.TTL.String()
	}
	if !o.Apply || md.TTL == "" || (!md.ExpiresAt.IsZero() && o.TTL == 0) {
		return
	}
	d, err := time.ParseDuration(md.TTL)
	if err != nil || d <= 0 {
		return
	}
	t := now.UTC().Add(d)
	if r := t.Truncate(time.Second); !r.Equal(t) {
		t = r.Add(time.Second)
	}
	md.ExpiresAt = t
}

// expired reports whether md has an expiry and it has passed at now.
func (md *Metadata) expired(now time.Time) bool {
	return !md.ExpiresAt.IsZero() && !now.Before(md.ExpiresAt)
}

// refuseProtected refuses destroying a protected env.
func (md *Metadata) refuseProtected() error {
	if !md.Protected {
		return nil
	}
	return errs.Refusedf("env %s is protected: run `province env protect %s --off` first", md.Name, md.Name)
}

// ProtectResult is the outcome of env protect.
type ProtectResult struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
}

// Protect sets (on) or clears an env's protection. It touches only the
// env's record.
func (m *Manager) Protect(name string, on bool) (*ProtectResult, error) {
	md, err := m.load(name)
	if err != nil {
		return nil, err
	}
	md.Protected = on
	if err := m.writeMetadata(m.dir(name), md); err != nil {
		return nil, err
	}
	return &ProtectResult{Name: name, Protected: on}, nil
}

// GC outcomes.
const (
	GCDestroyed = "destroyed"
	GCFailed    = "failed"
	GCSkipped   = "skipped"
)

// GCEnv is one expired env and what env gc did with it.
type GCEnv struct {
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	VMID        int       `json:"vmid,omitempty"`
	Instance    string    `json:"instance,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	Outcome     string    `json:"outcome"`
	Reason      string    `json:"reason,omitempty"` // why it was skipped
	Error       string    `json:"error,omitempty"`
	Code        errs.Kind `json:"code,omitempty"`
	GuestOutput string    `json:"guest_output,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
}

// GCResult is the outcome of env gc --yes, or of a dry run with nothing to
// destroy.
type GCResult struct {
	Envs   []GCEnv `json:"envs"`
	Failed int     `json:"failed"`
}

// GC destroys every expired env that is not protected, through Destroy,
// going on past failures. Without yes it refuses, naming what it would
// destroy, unless there is nothing to destroy. With failures it returns
// the result and an error.
func (m *Manager) GC(ctx context.Context, yes bool) (*GCResult, error) {
	mds, err := m.allMetadata()
	if err != nil {
		return nil, err
	}
	now := m.Now()
	res := &GCResult{Envs: []GCEnv{}}
	var doomed, protected []string
	for _, md := range mds {
		if !md.expired(now) {
			continue
		}
		e := GCEnv{Name: md.Name, Host: md.Host, VMID: md.VMID, Instance: md.Instance, ExpiresAt: md.ExpiresAt}
		if md.Protected {
			e.Outcome, e.Reason = GCSkipped, "protected"
			protected = append(protected, md.Name)
		} else {
			doomed = append(doomed, md.Name)
		}
		res.Envs = append(res.Envs, e)
	}
	if !yes && len(doomed) > 0 {
		msg := fmt.Sprintf("env gc destroys %d expired env(s) and their disks: %s", len(doomed), strings.Join(doomed, ", "))
		if len(protected) > 0 {
			msg += fmt.Sprintf(" (skipping protected: %s)", strings.Join(protected, ", "))
		}
		return nil, errs.Refusedf("%s: pass --yes", msg)
	}
	if !yes {
		return res, nil
	}
	for i := range res.Envs {
		e := &res.Envs[i]
		if e.Outcome == GCSkipped {
			continue
		}
		m.logf("destroying expired env %s (expired %s)", e.Name, e.ExpiresAt.Format(time.RFC3339))
		d, err := m.Destroy(ctx, e.Name, true)
		if err != nil {
			e.Outcome, e.Error, e.Code, e.GuestOutput = GCFailed, err.Error(), errs.KindOf(err), errs.GuestOutput(err)
			res.Failed++
			m.logf("warning: env gc: %s: %v", e.Name, err)
			continue
		}
		e.Outcome, e.Warnings = GCDestroyed, d.Warnings
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("env gc: %d of %d expired env(s) could not be destroyed", res.Failed, len(doomed))
	}
	return res, nil
}

// allMetadata returns the record of every env in state_dir, by name.
func (m *Manager) allMetadata() ([]*Metadata, error) {
	entries, err := os.ReadDir(m.Cfg.StateDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var mds []*Metadata
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
		mds = append(mds, md)
	}
	sort.Slice(mds, func(i, j int) bool { return mds[i].Name < mds[j].Name })
	return mds, nil
}
