// Package channelplugins is the composition root for bundled channel plugins.
// The platform uses the same Provider contract for bundled and installed plugins.
package channelplugins

import (
	"siagent/internal/channelplugins/wecomapp"
	"siagent/internal/channelplugins/wecombot"
	"siagent/internal/channels"
)

func Bundled() []channels.Plugin {
	return []channels.Plugin{
		func(options channels.PluginOptions) (channels.Provider, error) {
			return wecombot.New(options.Node, options.WorkerScript)
		},
		func(channels.PluginOptions) (channels.Provider, error) {
			return wecomapp.New(), nil
		},
	}
}
