package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"go.universe.tf/virtuakube"
)

type universeFunc func(*virtuakube.Universe) error

func withUniverse(do universeFunc) func(*cobra.Command, []string) {
	return func(_ *cobra.Command, _ []string) {
		if err := runDoWithUniverse(do); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}
}

func runDoWithUniverse(do universeFunc) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalCtx, signalCancel := context.WithCancel(ctx)
	defer signalCancel()

	// Handle ctrl+C by cancelling the context, which will shut down
	// everything in the universe.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		defer signalCancel()
		select {
		case <-stop:
		case <-signalCtx.Done():
		}
	}()

	start := time.Now()

	u, err := openOrCreateUniverse(ctx, rootFlags.universeDir)
	if err != nil {
		return fmt.Errorf("Getting universe: %v", err)
	}
	defer u.Close()

	if err := do(u); err != nil {
		return err
	}

	if rootFlags.wait {
		d := time.Since(start)
		switch {
		case d < time.Second:
			d = d.Truncate(time.Millisecond)
		case d < time.Second:
			d = d.Truncate(time.Second / 10)
		default:
			d = d.Truncate(time.Second)
		}
		fmt.Printf("Done (took %s). Resources available:\n\n", d)
		for _, cluster := range u.Clusters() {
			fmt.Printf("  Cluster %q: export KUBECONFIG=%q\n", cluster.Name(), cluster.Kubeconfig())
		}
		for _, vm := range u.VMs() {
			fmt.Printf("  VM %q: ssh -p%d root@localhost\n", vm.Hostname(), vm.ForwardedPort(22))
		}

		fmt.Println("\nHit ctrl+C to shut down")
		<-signalCtx.Done()
	}

	if rootFlags.save {
		fmt.Println("Saving universe...")
		if err := u.Save(); err != nil {
			return fmt.Errorf("Saving universe: %v", err)
		}
	} else {
		fmt.Println("Closing (and reverting) universe...")
		if err := u.Close(); err != nil {
			return fmt.Errorf("Closing universe: %v", err)
		}
	}

	return nil
}

// openOrCreateUniverse sets up a universe, either by creating it from
// scratch, or by opening an existing one.
func openOrCreateUniverse(ctx context.Context, dir string) (*virtuakube.Universe, error) {
	if dir == "" {
		return nil, errors.New("universe directory not specified")
	}

	cmd := virtuakube.Open
	_, err := os.Stat(dir)
	if os.IsNotExist(err) {
		cmd = virtuakube.Create
	} else if err != nil {
		return nil, err
	}

	universe, err := cmd(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("getting universe: %v", err)
	}

	return universe, nil
}
