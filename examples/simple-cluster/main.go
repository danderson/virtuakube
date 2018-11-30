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
	start := time.Now()
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
			Image:     *baseImg,
			MemoryMiB: *memory,
			PortForwards: map[int]bool{
				22: true,
			},
			CommandLog: os.Stdout,
		},
		NetworkAddon: *networkAddon,
	})
	if err != nil {
		return fmt.Errorf("Creating cluster: %v", err)
	}

	if err = cluster.Start(); err != nil {
		return fmt.Errorf("Starting cluster: %v", err)
	}

	fmt.Printf(`Cluster is up, took %s. To talk to Kubernetes:

export KUBECONFIG=%s

SSH ports for debugging (password is "root"):

controller: ssh -p%d root@localhost
      node: ssh -p%d root@localhost

`, time.Since(start), cluster.Kubeconfig(), cluster.Controller().ForwardedPort(22), cluster.Nodes()[0].ForwardedPort(22))

	if err := universe.Wait(context.Background()); err != nil {
		return fmt.Errorf("Waiting for universe to end: %v", err)
	}

	fmt.Println("Shutting down.")
	return nil
}
