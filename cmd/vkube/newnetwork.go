package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.universe.tf/virtuakube"
)

var newnetworkCmd = &cobra.Command{
	Use:   "newnetwork",
	Short: "Create a virtual network",
	Args:  cobra.NoArgs,
	Run:   withUniverse(&networkFlags.universe, newnetwork),
}

var networkFlags = struct {
	universe universeFlags
	name     string
}{}

func init() {
	rootCmd.AddCommand(newnetworkCmd)
	addUniverseFlags(newnetworkCmd, &networkFlags.universe, false, true)
	newnetworkCmd.Flags().StringVar(&networkFlags.name, "name", "", "name for the VM")
}

func newnetwork(u *virtuakube.Universe) error {
	cfg := &virtuakube.NetworkConfig{
		Name: networkFlags.name,
	}

	fmt.Printf("Creating network %q...\n", networkFlags.name)

	if err := u.NewNetwork(cfg); err != nil {
		return fmt.Errorf("Creating network: %v", err)
	}

	fmt.Printf("Created network %q\n", networkFlags.name)

	return nil
}
