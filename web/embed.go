// Package webassets embeds the HTML templates and static files.
package webassets

import "embed"

// FS holds templates/ and static/.
//
//go:embed templates static
var FS embed.FS
