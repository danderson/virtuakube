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
	baseImg = flag.String("vm-img", "virtuakube.qcow2", "VM base image")
	memory  = flag.Int("memory", 1024, "amount of memory per VM, in MiB")
	display = flag.Bool("display", false, "create display windows for each VM")
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

	vm, err := universe.NewVM(&virtuakube.VMConfig{
		BackingImagePath: *baseImg,
		MemoryMiB:        *memory,
		Display:          *display,
		PortForwards: map[int]bool{
			22: true,
		},
	})
	if err != nil {
		return fmt.Errorf("Creating VM: %v", err)
	}

	if err = vm.Start(); err != nil {
		return fmt.Errorf("Starting VM: %v", err)
	}

	fmt.Printf(`VM is starting up. SSH access (password is "root"):

ssh -p%d root@localhost

Waiting for VM to come up...
`, vm.ForwardedPort(22))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := vm.WaitReady(ctx); err != nil {
		return fmt.Errorf("Waiting for VM to be ready: %v", err)
	}

	fmt.Printf(`
VM is running.
Hit ctrl+C to shut down.
`)

	if err := universe.Wait(context.Background()); err != nil {
		return fmt.Errorf("Waiting for universe to end: %v", err)
	}

	fmt.Println("Shutting down.")
	return nil
}
