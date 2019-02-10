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
	name     string
	memory   int
	networks []string
}{}

func addVMFlags(cmd *cobra.Command) {
}

func init() {
	rootCmd.AddCommand(newvmCmd)
	addUniverseFlags(newvmCmd, &vmFlags.universe, true, false)
	newvmCmd.Flags().StringVar(&vmFlags.image, "image", "", "base disk image to use")
	newvmCmd.Flags().StringVar(&vmFlags.name, "name", "", "name for the VM")
	newvmCmd.Flags().IntVar(&vmFlags.memory, "memory", 1024, "amount of memory to give the VM in GiB")
	newvmCmd.Flags().StringSliceVar(&vmFlags.networks, "networks", []string{}, "networks to attach the VM to")
}

func newvm(u *virtuakube.Universe, verbose bool) error {
	cfg := &virtuakube.VMConfig{
		Name:      vmFlags.name,
		Image:     vmFlags.image,
		MemoryMiB: vmFlags.memory,
		Networks:  vmFlags.networks,
	}
	if verbose {
		cfg.CommandLog = os.Stdout
	}

	fmt.Printf("Creating VM %q...\n", vmFlags.name)

	vm, err := u.NewVM(cfg)
	if err != nil {
		return fmt.Errorf("Creating VM: %v", err)
	}
	if err = vm.Start(); err != nil {
		return fmt.Errorf("Starting VM: %v", err)
	}

	fmt.Printf("Created VM %q\n", vmFlags.name)

	return nil
}
