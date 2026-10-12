package main

import (
	"os"

	"github.com/C1ph3rX13/Aether/internal/version"
	"github.com/spf13/cobra"
)

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:     "aether",
		Version: version.Version,
		Short:   "Aether — AI general-purpose state-space search engine",
	}
	command.AddCommand(
		newServeCommand(),
		newDispatchCommand(),
		newRunCommand(),
	)
	return command
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
