package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go.universe.tf/virtuakube"
)

var (
	baseImg      = flag.String("vm-img", "virtuakube.qcow2", "VM base image")
	memory       = flag.Int("memory", 1024, "amount of memory per VM, in MiB")
	display      = flag.Bool("display", false, "create display windows for each VM")
	networkAddon = flag.String("network-addon", "calico", "network addon to install")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	universe, err := virtuakube.New(context.Background())
	if err != nil {
		return fmt.Errorf("Creating universe: %v", err)
	}
	defer universe.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		select {
		case <-stop:
			universe.Close()
		case <-universe.Context().Done():
		}
	}()

	cluster, err := universe.NewCluster(&virtuakube.ClusterConfig{
		NumNodes: 1,
		VMConfig: &virtuakube.VMConfig{
			BackingImagePath: *baseImg,
			MemoryMiB:        *memory,
			Display:          *display,
			PortForwards: map[int]bool{
				22: true,
			},
		},
		NetworkAddon: *networkAddon,
	})
	if err != nil {
		return fmt.Errorf("Creating cluster: %v", err)
	}

	if err = cluster.Start(); err != nil {
		return fmt.Errorf("Starting cluster: %v", err)
	}

	fmt.Printf(`Cluster is starting up. SSH ports for debugging (password is "root"):

controller: ssh -p%d root@localhost
      node: ssh -p%d root@localhost

Waiting for cluster to come up...
`, cluster.Controller().ForwardedPort(22), cluster.Nodes()[0].ForwardedPort(22))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := cluster.WaitReady(ctx); err != nil {
		return fmt.Errorf("Waiting for cluster to be ready: %v", err)
	}

	fmt.Printf(`
Cluster is running. To talk to Kubernetes:

export KUBECONFIG=%s

Hit ctrl+C to shut down.
`, cluster.Kubeconfig())

	if err := universe.Wait(context.Background()); err != nil {
		return fmt.Errorf("Waiting for universe to end: %v", err)
	}

	fmt.Println("Shutting down.")
	return nil
}
