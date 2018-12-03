package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"

	"go.universe.tf/virtuakube"
)

var (
	dir          = flag.String("universe-dir", "", "directory in which to place the universe")
	baseImg      = flag.String("vm-img", "virtuakube.qcow2", "VM base image")
	memory       = flag.Int("memory", 1024, "amount of memory per VM, in MiB")
	nodes        = flag.Int("nodes", 1, "number of worker nodes in addition to master")
	display      = flag.Bool("display", false, "create display windows for each VM")
	networkAddon = flag.String("network-addon", "calico", "network addon to install")
	kvm          = flag.Bool("kvm", true, "use KVM hardware acceleration")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	udir := *dir
	if udir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		tmp, err := ioutil.TempDir(wd, "vkube")
		if err != nil {
			return err
		}
		udir = tmp
	}

	universe, err := virtuakube.New(context.Background(), udir)
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
		Name:     "example",
		NumNodes: *nodes,
		VMConfig: &virtuakube.VMConfig{
			Image:        *baseImg,
			MemoryMiB:    *memory,
			PortForwards: map[int]bool{22: true},
			CommandLog:   os.Stdout,
			NoKVM:        !*kvm,
		},
		NetworkAddon: *networkAddon,
	})
	if err != nil {
		return fmt.Errorf("Creating cluster: %v", err)
	}

	if err = cluster.Start(); err != nil {
		return fmt.Errorf("Starting cluster: %v", err)
	}

	fmt.Printf(`Cluster is up. To talk to Kubernetes:

	export KUBECONFIG=%s

	SSH ports for debugging (password is "root"):

	controller: ssh -p%d root@localhost
	`, cluster.Kubeconfig(), cluster.Controller().ForwardedPort(22))
	for i, vm := range cluster.Nodes() {
		fmt.Printf("node %d: ssh -p%d root@localhost\n", i+1, vm.ForwardedPort(22))
	}
	fmt.Println("\nHit ctrl+C to shut down.")

	if err := universe.Wait(context.Background()); err != nil {
		return fmt.Errorf("waiting for universe to end: %v", err)
	}

	return nil
}
