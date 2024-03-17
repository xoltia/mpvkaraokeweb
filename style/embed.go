package style

import (
	"embed"
	_ "embed"
)

//go:embed *.css
var CSSFiles embed.FS
