// Package move moves an existing guest between two standalone Proxmox
// hosts: stop on the source, stream a vzdump to the target, restore with
// the same VMID, and leave the source stopped with onboot=0.
package move

import (
	"fmt"
	"time"

	"github.com/wellavelino/province/internal/proxmox"
)

// DumpName is the standard vzdump archive name, so PVE tools recognise it.
func DumpName(typ string, vmid int, t time.Time) string {
	ext := "tar.zst"
	if typ == proxmox.TypeQEMU {
		ext = "vma.zst"
	}
	return fmt.Sprintf("vzdump-%s-%d-%s.%s", typ, vmid, t.Format("2006_01_02-15_04_05"), ext)
}
