// Command province creates, lists, destroys and moves environments on Proxmox VE.
package main

import (
	"os"

	"github.com/wellavelino/province/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
