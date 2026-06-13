// Command shipmono-agent is the ShipMono deploy agent that runs on customer
// Ubuntu LTS servers. It speaks the control plane's /agent/v1 contract and
// performs the fixed verb set (deploy, rollback, reload, restore, add_domain,
// remove_domain, status) under the host's scoped sudoers and systemd sandbox.
package main

import (
	"os"

	"github.com/wilihandarwo/shipmono-agent/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
