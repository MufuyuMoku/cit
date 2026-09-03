package cmd

import "context"

// App is the object bound to the frontend. It is deliberately empty: the
// scaffold exposes build metadata and nothing else.
type App struct {
	ctx context.Context
}

// NewApp returns an unstarted App.
func NewApp() *App {
	return &App{}
}

// startup is called by Wails once the window exists and the runtime is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// GetBuildInfo reports the version of the running application.
func (a *App) GetBuildInfo() BuildInfo {
	return buildInfo()
}
