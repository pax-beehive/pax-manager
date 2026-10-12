// Render the actual production bootstrap for cross-repository shell tests.
package main

import (
	"fmt"
	"os"

	"github.com/pax-beehive/pax-manager/internal/manager/paxd"
)

func main() {
	if len(os.Args) != 3 {
		panic("usage: render_setup DOWNLOAD_ORIGIN MACHINE_ORIGIN")
	}
	script, err := (paxd.SetupConfig{DownloadURL: os.Args[1], MachineURL: os.Args[2]}).Script()
	if err != nil {
		panic(err)
	}
	fmt.Print(script)
}
