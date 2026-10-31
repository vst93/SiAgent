// Package channelplugins is the composition root for bundled channel plugins.
// The platform uses the same Provider contract for bundled and installed plugins.
package channelplugins

import (
	"siagent/internal/channelplugins/feishubot"
	"siagent/internal/channelplugins/qqbot"
	"siagent/internal/channelplugins/wecomapp"
	"siagent/internal/channelplugins/wecombot"
	"siagent/internal/channelplugins/weixin"
	"siagent/internal/channels"
)

func Bundled() []channels.Plugin {
	return []channels.Plugin{
		func(options channels.PluginOptions) (channels.Provider, error) {
			return wecombot.New(options.Node, options.WorkerScript)
		},
		func(options channels.PluginOptions) (channels.Provider, error) {
			// The Feishu worker sits beside the WeCom entrypoint in the same
			// runtime/dist tree, so `-channel-worker` keeps working unchanged.
			return feishubot.New(options.Node, options.Worker("feishu-bot"))
		},
		func(options channels.PluginOptions) (channels.Provider, error) {
			return qqbot.New(options.Node, options.Worker("qqbot"))
		},
		func(channels.PluginOptions) (channels.Provider, error) {
			return weixin.New(), nil
		},
		func(channels.PluginOptions) (channels.Provider, error) {
			return wecomapp.New(), nil
		},
	}
}
