// Package cmd wires the CIT application together and hands it to Wails.
package cmd

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Run starts the desktop application and blocks until the window closes.
// assets is the embedded, already-built frontend.
func Run(assets embed.FS) {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:            "CIT",
		Width:            1024,
		Height:           700,
		MinWidth:         640,
		MinHeight:        480,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 18, G: 18, B: 20, A: 1},
		OnStartup:        app.startup,
		Bind: []any{
			app,
		},
	})
	if err != nil {
		log.Fatalf("cit: %v", err)
	}
}
