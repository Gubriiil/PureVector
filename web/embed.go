// Package web embeds the browser UI (dashboard and calibration task).
package web

import "embed"

// Files holds index.html, calibrate.html, app.css and js/*.js.
//
//go:embed index.html calibrate.html app.css favicon.svg js/*.js
var Files embed.FS
