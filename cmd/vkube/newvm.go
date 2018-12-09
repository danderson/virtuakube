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
	Run:   withUniverse(newvm),
}

var vmFlags = struct {
	image    string
	hostname string
	memory   int
}{}

func addVMFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&vmFlags.image, "image", "", "base disk image to use")
	cmd.Flags().StringVar(&vmFlags.hostname, "hostname", "", "hostname for the VM")
	cmd.Flags().IntVar(&vmFlags.memory, "memory", 1024, "amount of memory to give the VM in GiB")
}

func init() {
	rootCmd.AddCommand(newvmCmd)
	addRootFlags(newvmCmd, true, false)
	addVMFlags(newvmCmd)
}

func newvm(u *virtuakube.Universe) error {
	cfg := &virtuakube.VMConfig{
		ImageName: vmFlags.image,
		Hostname:  vmFlags.hostname,
		MemoryMiB: vmFlags.memory,
	}
	if rootFlags.verbose {
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

	return nil
}
