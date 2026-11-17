// This module composes the pinned Prusa Connect release with the local Panda Connect driver.
package main

import (
	"os"

	"github.com/prusa3d/prusa-connect/compose"
	_ "github.com/skaronator/panda-connect/connector"
	_ "github.com/skaronator/panda-connect/server/internal/bambucamera"
)

func main() { os.Exit(compose.Main()) }
