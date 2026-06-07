package templates

import "embed"

//go:embed manifests/*.go.tmpl
var Manifests embed.FS
