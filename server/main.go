// This module composes the pinned Prusa Connect release with the local PandaLink driver.
package main

import (
	"os"

	"github.com/prusa3d/prusa-connect/compose"
	_ "github.com/skaronator/pandalink"
	_ "github.com/skaronator/pandalink/server/internal/h3ccamera"
)

func main() { os.Exit(compose.Main()) }
