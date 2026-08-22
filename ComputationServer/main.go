// Command cserver runs the CloudAC computation server.
//
// It reads its configuration from the environment. See sys.LoadConfig for the
// variables, and note that it refuses to start on a non-loopback address
// without CLOUDAC_SHARED_SECRET set.
package main

import (
	"log/slog"
	"os"

	cserver "github.com/0xntpower/CloudAC/ComputationServer/sys"
)

// version is injected at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// It defaults to "dev" so an un-stamped build is obvious rather than pretending
// to be a release.
var version = "dev"

func main() {
	if err := cserver.Start(version); err != nil {
		slog.Error("computation server stopped", "err", err)
		os.Exit(1)
	}
}
