// Package adminui embeds the built admin dashboard (web/admin, a Vite/React
// app). dist/ is the committed production build: `npm run build` in web/admin
// writes here, so the server binary is self-contained.
package adminui

import "embed"

//go:embed all:dist
var Dist embed.FS
