// Package scripts provides the reviewed installer to the application updater.
package scripts

import _ "embed"

//go:embed install.sh
var Installer string
