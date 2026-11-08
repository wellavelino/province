package tfgen

import (
	"path"
	"strings"
)

// OSType derives the provider's operating_system.type from a template
// volume id such as local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst.
// Unknown distributions are "unmanaged": PVE then leaves the guest's
// network and hostname files alone, which is safe but means DHCP must be
// configured by the template itself.
func OSType(template string) string {
	file := strings.ToLower(path.Base(template))
	for _, os := range []string{"debian", "ubuntu"} {
		if strings.HasPrefix(file, os+"-") || strings.HasPrefix(file, os+"_") {
			return os
		}
	}
	return "unmanaged"
}
