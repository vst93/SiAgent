package main

import (
	"bytes"
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"
)

func TestCommandsPreserveLauncherOptionsAndRestartAsServer(t *testing.T) {
	defaults := installation{DataDir: "data", Node: "node", Runtime: "runtime/dist/index.js", ChannelWorker: "runtime/dist/channel-wecom.js"}
	for _, tc := range []struct {
		args                  []string
		command, data, listen string
		restart               []string
	}{
		{nil, "serve", "data", "127.0.0.1:8080", []string{"serve"}},
		{[]string{"-data-dir", "/installer/data", "update", "--listen", "127.0.0.1:9090"}, "update", "/installer/data", "127.0.0.1:9090", []string{"serve", "-data-dir", "/installer/data", "--listen", "127.0.0.1:9090"}},
		{[]string{"serve", "-data-dir", "space path"}, "serve", "space path", "127.0.0.1:8080", []string{"serve", "-data-dir", "space path"}},
		{[]string{"-listen", "127.0.0.1:9000"}, "serve", "data", "127.0.0.1:9000", []string{"serve", "-listen", "127.0.0.1:9000"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Setenv("SIAGENT_DATA_DIR", "")
			t.Setenv("SIAGENT_LISTEN", "")
			options, err := parseCommand(tc.args, defaults, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if options.Command != tc.command || options.DataDir != tc.data || options.Listen != tc.listen || !reflect.DeepEqual(options.ServerArgs, tc.restart) {
				t.Fatalf("unexpected command: %+v", options)
			}
		})
	}
}

func TestServiceCommandParsing(t *testing.T) {
	defaults := installation{DataDir: "data", Node: "node", Runtime: "runtime/dist/index.js", ChannelWorker: "runtime/dist/channel-wecom.js"}
	for _, tc := range []struct {
		args   []string
		action string
	}{
		{[]string{"service", "install"}, "install"},
		{[]string{"-data-dir", "/installer/data", "service", "status"}, "status"},
	} {
		options, err := parseCommand(tc.args, defaults, &bytes.Buffer{})
		if err != nil {
			t.Fatal(tc.args, err)
		}
		if options.Command != "service" || options.ServiceAction != tc.action {
			t.Fatalf("unexpected service command: %+v", options)
		}
	}
	if _, err := parseCommand([]string{"service"}, defaults, &bytes.Buffer{}); err == nil {
		t.Fatal("service without action accepted")
	}
}

func TestHelpAliasesWorkBeforeOrAfterInstallerFlags(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"-help"}, {"-data-dir", "/installer/data", "help"}, {"update", "-h"}} {
		var output bytes.Buffer
		_, err := parseCommand(args, installation{}, &output)
		if !errors.Is(err, flag.ErrHelp) || !strings.Contains(output.String(), "siagent update") || !strings.Contains(output.String(), "serve") {
			t.Fatal(args, err, output.String())
		}
	}
	for _, args := range [][]string{{"unknown"}, {"serve", "extra"}, {"update", "--probe", "text"}} {
		if _, err := parseCommand(args, installation{}, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
}
