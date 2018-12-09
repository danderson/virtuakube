package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.universe.tf/virtuakube"
)

var newclusterCmd = &cobra.Command{
	Use:   "newcluster",
	Short: "Create a Kubernetes cluster",
	Args:  cobra.NoArgs,
	Run:   withUniverse(&clusterFlags.universe, newcluster),
}

var clusterFlags = struct {
	universe universeFlags
	name     string
	nodes    int
	image    string
	memory   int
	addon    string
}{}

func init() {
	rootCmd.AddCommand(newclusterCmd)
	addUniverseFlags(newclusterCmd, &clusterFlags.universe, true, false)
	addVMFlags(newclusterCmd)
	newclusterCmd.Flags().StringVar(&clusterFlags.name, "name", "", "name for the new cluster")
	newclusterCmd.Flags().IntVar(&clusterFlags.nodes, "nodes", 1, "number of nodes in the cluster")
	newclusterCmd.Flags().StringVar(&clusterFlags.addon, "network-addon", "calico", "network addon to install")
	newclusterCmd.Flags().StringVar(&clusterFlags.image, "image", "", "base disk image to use")
	newclusterCmd.Flags().IntVar(&clusterFlags.memory, "memory", 1024, "amount of memory to give the VMs in GiB")
}

func newcluster(u *virtuakube.Universe, verbose bool) error {
	cfg := &virtuakube.ClusterConfig{
		Name:     clusterFlags.name,
		NumNodes: clusterFlags.nodes,
		VMConfig: &virtuakube.VMConfig{
			ImageName: clusterFlags.image,
			MemoryMiB: clusterFlags.memory,
		},
		NetworkAddon: clusterFlags.addon,
	}
	if verbose {
		cfg.VMConfig.CommandLog = os.Stdout
	}

	fmt.Printf("Creating cluster %q...\n", clusterFlags.name)

	vm, err := u.NewCluster(cfg)
	if err != nil {
		return fmt.Errorf("Creating cluster: %v", err)
	}
	if err = vm.Start(); err != nil {
		return fmt.Errorf("Starting cluster: %v", err)
	}

	fmt.Printf("Created cluster %q\n", clusterFlags.name)

	return nil
}
