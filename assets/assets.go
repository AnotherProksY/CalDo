// Package assets embeds CalDo's logo. logo.svg is the source; regenerate the
// PNGs with `make logo` after changing it.
package assets

import _ "embed"

// NotifyIcon is the 128px logo shown in desktop notifications.
//
//go:embed logo-128.png
var NotifyIcon []byte
