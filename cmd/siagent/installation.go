package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type installation struct {
	Node, Runtime, ChannelWorker, DataDir string
	Bundled                               bool
}

func locateInstallation(executable string) installation {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	base := filepath.Dir(executable)
	// Retain release defaults even when the private Node directory was lost.
	_, markerErr := os.Stat(filepath.Join(base, "NODE_VERSION"))
	privateDir, privateErr := os.Stat(filepath.Join(base, "libexec"))
	if markerErr == nil || (privateErr == nil && privateDir.IsDir()) {
		return installation{
			Node:          filepath.Join(base, "libexec", "node"),
			Runtime:       filepath.Join(base, "runtime", "dist", "index.js"),
			ChannelWorker: filepath.Join(base, "runtime", "dist", "channel-wecom.js"),
			DataDir:       filepath.Join(base, "data"),
			Bundled:       true,
		}
	}
	return installation{
		Node: "node", Runtime: "runtime/dist/index.js",
		ChannelWorker: "runtime/dist/channel-wecom.js", DataDir: "data",
	}
}

func checkBundledRuntime(node, script string) error {
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return fmt.Errorf("runtime entrypoint %q is missing; extract the complete SiAgent release package", script)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", `if (Number(process.versions.node.split(".")[0]) < 24) process.exit(1); require("node:sqlite")`)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cannot use Node.js runtime %q (requires Node.js 24+ with node:sqlite); extract the complete release or set -node / SIAGENT_NODE: %w", node, err)
	}
	return nil
}
