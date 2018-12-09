package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.universe.tf/virtuakube"
)

var newvmCmd = &cobra.Command{
	Use:   "newvm",
	Short: "Create a standalone VM",
	Args:  cobra.NoArgs,
	Run:   withUniverse(&vmFlags.universe, newvm),
}

var vmFlags = struct {
	universe universeFlags
	image    string
	hostname string
	memory   int
}{}

func addVMFlags(cmd *cobra.Command) {
}

func init() {
	rootCmd.AddCommand(newvmCmd)
	addUniverseFlags(newvmCmd, &vmFlags.universe, true, false)
	newvmCmd.Flags().StringVar(&vmFlags.image, "image", "", "base disk image to use")
	newvmCmd.Flags().StringVar(&vmFlags.hostname, "hostname", "", "hostname for the VM")
	newvmCmd.Flags().IntVar(&vmFlags.memory, "memory", 1024, "amount of memory to give the VM in GiB")
}

func newvm(u *virtuakube.Universe, verbose bool) error {
	cfg := &virtuakube.VMConfig{
		ImageName: vmFlags.image,
		Hostname:  vmFlags.hostname,
		MemoryMiB: vmFlags.memory,
	}
	if verbose {
		cfg.CommandLog = os.Stdout
	}

	fmt.Printf("Creating VM %q...\n", vmFlags.hostname)

	vm, err := u.NewVM(cfg)
	if err != nil {
		return fmt.Errorf("Creating VM: %v", err)
	}
	if err = vm.Start(); err != nil {
		return fmt.Errorf("Starting VM: %v", err)
	}

	fmt.Printf("Created VM %q\n", vmFlags.hostname)

	return nil
}
