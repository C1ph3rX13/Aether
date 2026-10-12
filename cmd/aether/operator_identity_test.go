package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestProductionCommandsRequireExplicitBootstrapManifest(t *testing.T) {
	for _, command := range []*cobra.Command{newServeCommand(), newDispatchCommand(), newRunCommand()} {
		if flag := command.Flags().Lookup("deployment"); flag == nil {
			t.Fatalf("%s does not expose --deployment", command.Name())
		}
		if flag := command.Flags().Lookup("config"); flag != nil {
			t.Fatalf("%s exposes obsolete --config", command.Name())
		}
		if flag := command.Flags().Lookup("database"); flag != nil {
			t.Fatalf("%s exposes obsolete --database", command.Name())
		}
		if flag := command.Flags().Lookup("host"); flag != nil {
			t.Fatalf("%s exposes obsolete --host", command.Name())
		}
		if flag := command.Flags().Lookup("port"); flag != nil {
			t.Fatalf("%s exposes obsolete --port", command.Name())
		}
	}
}
