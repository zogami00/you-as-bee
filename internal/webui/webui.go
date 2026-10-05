package webui

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

// assets holds the vanilla HTML/CSS/JS shell shared by the Windows client and
// the Pi agent. It is embedded with no build step and no third-party runtime.
//
//go:embed assets
var assetsFS embed.FS

var (
	indexTmpl = template.Must(template.ParseFS(assetsFS, "assets/index.html"))
	loginTmpl = template.Must(template.ParseFS(assetsFS, "assets/login.html"))
)

// Page is the data injected into index.html when the shell is rendered.
type Page struct {
	// Mode is "client" or "agent"; it selects the JavaScript adapter.
	Mode string
	// APIBase is the path the UI calls, normally "/v1".
	APIBase string
}

// LoginPage is the data injected into login.html.
type LoginPage struct {
	// Code is a freshly issued one-time code, echoed back on submit.
	Code string
	// Error is a human-readable failure message, empty on the first render.
	Error string
}

// StaticHandler serves the embedded asset files (app.css, app.js, ...). Mount
// it under the "/ui/assets/" prefix.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		// The embed directive guarantees this; a failure is a programmer error.
		panic("webui: embedded assets: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}

// RenderIndex writes the application shell with p injected.
func RenderIndex(w http.ResponseWriter, p Page) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	return indexTmpl.Execute(w, p)
}

// RenderLogin writes the token form with p injected and the given HTTP status.
func RenderLogin(w http.ResponseWriter, p LoginPage, status int) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return loginTmpl.Execute(w, p)
}
