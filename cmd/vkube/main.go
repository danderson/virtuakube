package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var rootFlags = struct {
	universeDir string
	verbose     bool
	wait        bool
	save        bool
}{}

func addRootFlags(cmd *cobra.Command, wait, save bool) {
	cmd.Flags().StringVarP(&rootFlags.universeDir, "universe", "u", "", "directory containing the universe")
	cmd.Flags().BoolVarP(&rootFlags.verbose, "verbose", "v", false, "show commands being executed under the hood")
	cmd.Flags().BoolVarP(&rootFlags.wait, "wait", "w", wait, "wait for ctrl+C before exiting")
	cmd.Flags().BoolVarP(&rootFlags.save, "save", "s", save, "save the universe on exit")
	cmd.MarkFlagRequired("universe")
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "vkube",
	Short: "A toolkit for creating virtual Kubernetes clusters (and other things)",
}
