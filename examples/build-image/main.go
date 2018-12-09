package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"go.universe.tf/virtuakube"
)

var (
	dir = flag.String("universe-dir", "", "directory in which to place the universe")
	kvm = flag.Bool("kvm", true, "use KVM hardware acceleration")
)

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if *dir == "" {
		return fmt.Errorf("-universe-dir is required (but will be created if non-existent")
	}

	cmd := virtuakube.Open
	_, err := os.Stat(*dir)
	if os.IsNotExist(err) {
		cmd = virtuakube.Create
	} else if err != nil {
		return err
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

	fmt.Println("Creating universe...")

	universe, err := cmd(ctx, *dir)
	if err != nil {
		return fmt.Errorf("Creating universe: %v", err)
	}
	defer universe.Close()

	cfg := &virtuakube.ImageConfig{
		Name: "example",
		CustomizeFuncs: []virtuakube.ImageCustomizeFunc{
			virtuakube.CustomizeInstallK8s,
			virtuakube.CustomizePreloadK8sImages,
		},
		BuildLog: os.Stdout,
		NoKVM:    !*kvm,
	}
	if _, err := universe.NewImage(cfg); err != nil {
		return fmt.Errorf("creating image: %q", err)
	}

	// Closing the universe will delete the image, since it wasn't
	// there at the start of the session. Instead, we save.
	if err := universe.Save(); err != nil {
		return fmt.Errorf("saving universe: %q", err)
	}

	return nil
}
