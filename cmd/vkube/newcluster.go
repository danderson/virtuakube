package main

import (
	"fmt"
	"os"
	"strings"

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
	universe     universeFlags
	name         string
	nodes        int
	image        string
	memory       int
	networkAddon string
	registry     bool
	addons       []string
	networks     []string
}{}

func init() {
	rootCmd.AddCommand(newclusterCmd)
	addUniverseFlags(newclusterCmd, &clusterFlags.universe, true, false)
	addVMFlags(newclusterCmd)
	newclusterCmd.Flags().StringVar(&clusterFlags.name, "name", "", "name for the new cluster")
	newclusterCmd.Flags().IntVar(&clusterFlags.nodes, "nodes", 1, "number of nodes in the cluster")
	newclusterCmd.Flags().StringVar(&clusterFlags.networkAddon, "network-addon", "calico", "network addon to install")
	newclusterCmd.Flags().BoolVar(&clusterFlags.registry, "install-registry", true, "install an in-cluster registry")
	newclusterCmd.Flags().StringSliceVar(&clusterFlags.addons, "addons", nil, "addons to install")
	newclusterCmd.Flags().StringVar(&clusterFlags.image, "image", "", "base disk image to use")
	newclusterCmd.Flags().IntVar(&clusterFlags.memory, "memory", 1024, "amount of memory to give the VMs in GiB")
	newclusterCmd.Flags().StringSliceVar(&clusterFlags.networks, "networks", []string{}, "networks to attach the VM to")
}

func newcluster(u *virtuakube.Universe, verbose bool) error {
	cfg := &virtuakube.ClusterConfig{
		Name:     clusterFlags.name,
		NumNodes: clusterFlags.nodes,
		VMConfig: &virtuakube.VMConfig{
			Image:     clusterFlags.image,
			MemoryMiB: clusterFlags.memory,
			Networks:  clusterFlags.networks,
		},
	}
	if verbose {
		cfg.VMConfig.CommandLog = os.Stdout
	}

	fmt.Printf("Creating cluster %q...\n", clusterFlags.name)

	cluster, err := u.NewCluster(cfg)
	if err != nil {
		return fmt.Errorf("Creating cluster: %v", err)
	}
	if err = cluster.Start(); err != nil {
		return fmt.Errorf("Starting cluster: %v", err)
	}

	if clusterFlags.networkAddon != "" {
		fmt.Printf("Installing network addon %q...\n", clusterFlags.networkAddon)
		if err := cluster.InstallNetworkAddon(clusterFlags.networkAddon); err != nil {
			return fmt.Errorf("Installing network addon: %v", err)
		}
	}

	if clusterFlags.registry {
		fmt.Println("Installing registry addon...")
		if err := cluster.InstallRegistry(); err != nil {
			return fmt.Errorf("Installing registry: %v", err)
		}
	}

	if len(clusterFlags.addons) != 0 {
		fmt.Printf("Installing addons %s...\n", strings.Join(clusterFlags.addons, ", "))
		for _, addon := range clusterFlags.addons {
			if err := cluster.ApplyManifest(addon); err != nil {
				return fmt.Errorf("installing addon %q: %v", addon, err)
			}
		}
	}

	fmt.Printf("Created cluster %q\n", clusterFlags.name)

	return nil
}
