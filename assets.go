// Package assets embeds the UI so the executable works from any directory.
package assets

import "embed"

//go:embed templates/*.html static/css/*.css
var Files embed.FS
