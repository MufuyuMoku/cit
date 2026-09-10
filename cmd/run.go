// Package cmd wires the CIT application together and hands it to Wails.
package cmd

import (
	"embed"
	"log"
	"net/http"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Run starts the desktop application and blocks until the window closes.
// assets is the embedded, already-built frontend.
func Run(assets embed.FS) {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "CIT",
		Width:     1180,
		Height:    780,
		MinWidth:  720,
		MinHeight: 520,
		AssetServer: &assetserver.Options{
			Assets: assets,
			// Thumbnails are served here rather than handed through the binding
			// layer as base64, so the webview can cache them and a long overview
			// scrolls without re-sending every picture.
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if app.svc == nil {
					http.NotFound(w, r)
					return
				}
				app.svc.thumbnailHandler().ServeHTTP(w, r)
			}),
		},
		BackgroundColour: &options.RGBA{R: 250, G: 249, B: 246, A: 1},
		OnStartup:        app.startup,
		// Watching is cancelled here, and the vault is not closed until the watch
		// loop has returned: closing CIT in the middle of a large file aborts that
		// store cleanly rather than leaving a half-written one behind.
		OnShutdown: app.shutdown,
		Bind: []any{
			app,
		},
	})
	if err != nil {
		log.Fatalf("cit: %v", err)
	}
}
