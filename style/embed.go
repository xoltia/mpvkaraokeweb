package style

import (
	"embed"
)

//go:embed *.css
var CSSFiles embed.FS
