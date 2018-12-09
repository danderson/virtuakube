package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go.universe.tf/virtuakube"
)

var (
	dir = flag.String("universe-dir", "", "directory in which to place the universe")
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

	if *dir == "" {
		return errors.New("must pass -universe-dir to thaw a universe")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle ctrl+C by cancelling the context, which will shut down
	// everything in the universe.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	fmt.Println("Thawing universe...")

	universe, err := virtuakube.Open(ctx, *dir)
	if err != nil {
		return fmt.Errorf("opening universe: %v", err)
	}
	defer universe.Close()

	fmt.Printf("Universe resumed in %s. Resources available:\n\n", time.Since(start).Truncate(time.Millisecond))
	for _, cluster := range universe.Clusters() {
		fmt.Printf("Cluster %q: export KUBECONFIG=%q\n", cluster.Name(), cluster.Kubeconfig())
	}
	fmt.Println("")
	for _, vm := range universe.VMs() {
		fmt.Printf("VM %q: ssh -p%d root@localhost\n", vm.Hostname(), vm.ForwardedPort(22))
	}
	fmt.Println("\nHit ctrl+C to shut down")

	if err := universe.Wait(context.Background()); err != nil {
		return fmt.Errorf("waiting for universe to end: %v", err)
	}

	fmt.Println("Shutting down...")

	return nil
}
