// Command cit is the Wails entry point for the CIT desktop application.
//
// This file exists at the module root because the Wails v2 CLI always compiles
// the root package, and because go:embed cannot reach outside its own package
// directory. It holds nothing but the embedded frontend and a hand-off to
// package cmd, which owns the actual application wiring.
package main

import (
	"embed"

	"github.com/MufuyuMoku/cit/cmd"
)

//go:embed all:frontend/build
var assets embed.FS

func main() {
	cmd.Run(assets)
}
