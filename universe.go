package virtuakube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var universeTools = []string{
	"vde_switch",
	"qemu-system-x86_64",
	"qemu-img",
}

// checkTools returns an error if a command required by virtuakube is
// not available on the system.
func checkTools(tools []string) error {
	missing := []string{}
	for _, tool := range tools {
		_, err := exec.LookPath(tool)
		if err != nil {
			if e, ok := err.(*exec.Error); ok && e.Err == exec.ErrNotFound {
				missing = append(missing, tool)
				continue
			}
			return err
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required tools missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

// A Universe is a virtual test network and its associated resources.
type Universe struct {
	// Root containing all the stuff in the universe.
	dir string
	// VDE switch control socket, used by VMs to attach to the switch.
	switchSock string

	// Context for the lifetime of the universe. It's canceled when
	// the universe is closed.
	ctx      context.Context
	shutdown context.CancelFunc

	// Network resources for the universe. VMs request these on
	// creation.
	nextPort int
	nextIPv4 net.IP
	nextIPv6 net.IP

	mu sync.Mutex

	// Resources in the universe: a virtual switch, some VMs, some k8s
	// clusters.
	swtch    *exec.Cmd
	vms      map[string]*VM
	clusters map[string]*Cluster

	// Records any close errors, so we can do concurrent-safe
	// shutdown.
	closed   bool
	closeErr error
}

type universeConfig struct {
	NextPort int
	NextIPv4 net.IP
	NextIPv6 net.IP
}

// New creates a new virtual universe. The ctx controls the overall
// lifetime of the universe, i.e. if the context is canceled or times
// out, the universe will be destroyed.
func New(ctx context.Context, dir string) (*Universe, error) {
	cfg := &universeConfig{
		NextPort: 50000,
		NextIPv4: net.ParseIP("172.20.0.1"),
		NextIPv6: net.ParseIP("fd00::1"),
	}

	if err := os.Mkdir(filepath.Join(dir, "vm"), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, "cluster"), 0700); err != nil {
		return nil, err
	}

	return mkUniverse(ctx, cfg, dir)
}

func Thaw(ctx context.Context, dir string) (*Universe, error) {
	bs, err := ioutil.ReadFile(filepath.Join(dir, "universe.json"))
	if err != nil {
		return nil, err
	}

	var cfg universeConfig
	if err := json.Unmarshal(bs, &cfg); err != nil {
		return nil, err
	}

	u, err := mkUniverse(ctx, &cfg, dir)
	if err != nil {
		return nil, err
	}

	// Thaw all VMs.
	f, err := os.Open(filepath.Join(u.dir, "vm"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vmPaths, err := f.Readdir(0)
	if err != nil {
		return nil, err
	}

	// thaw all VMs concurrently.
	res := make(chan error, len(vmPaths))
	for _, vmPath := range vmPaths {
		go func(name string) {
			vm, err := u.thawVM(name)
			if err != nil {
				res <- err
				return
			}
			if err := vm.boot(); err != nil {
				res <- err
				return
			}

			u.mu.Lock()
			defer u.mu.Unlock()
			u.vms[name] = vm
			res <- nil
		}(vmPath.Name())
	}
	for range vmPaths {
		if err := <-res; err != nil {
			return nil, err
		}
	}

	// Thaw all clusters
	f, err = os.Open(filepath.Join(u.dir, "cluster"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	clusterPaths, err := f.Readdir(0)
	if err != nil {
		return nil, err
	}

	for _, clusterPath := range clusterPaths {
		cluster, err := u.thawCluster(clusterPath.Name())
		if err != nil {
			return nil, fmt.Errorf("thawing cluster %q: %v", clusterPath.Name(), err)
		}
		u.clusters[clusterPath.Name()] = cluster
	}

	return u, nil
}

func mkUniverse(ctx context.Context, cfg *universeConfig, dir string) (*Universe, error) {
	if err := checkTools(universeTools); err != nil {
		return nil, err
	}

	ctx, shutdown := context.WithCancel(ctx)

	sock := filepath.Join(dir, "switch")

	ret := &Universe{
		dir:      dir,
		ctx:      ctx,
		shutdown: shutdown,
		nextPort: cfg.NextPort,
		nextIPv4: cfg.NextIPv4.To4(),
		nextIPv6: cfg.NextIPv6,
		vms:      map[string]*VM{},
		clusters: map[string]*Cluster{},
		swtch: exec.CommandContext(
			ctx,
			"vde_switch",
			"--sock", sock,
			"-m", "0600",
		),
		switchSock: sock,
	}

	ret.mu.Lock()
	defer ret.mu.Unlock()

	if err := ret.swtch.Start(); err != nil {
		shutdown()
		return nil, err
	}
	// Destroy the universe if the virtual switch exits.
	go func() {
		ret.swtch.Wait()
		ret.Close()
	}()
	// Destroy the universe if the parent context cancels.
	go func() {
		<-ctx.Done()
		ret.Close()
	}()

	return ret, nil
}

// Context returns a context that gets canceled when the universe is
// destroyed.
func (u *Universe) Context() context.Context {
	return u.ctx
}

// Close destroys the universe, freeing up processes and temporary
// files.
func (u *Universe) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return u.closeErr
	}
	u.closed = true
	u.shutdown()

	for _, dir := range []string{"switch", "vm", "cluster"} {
		if err := os.RemoveAll(filepath.Join(u.dir, dir)); err != nil {
			u.closeErr = err
		}
	}

	return u.closeErr
}

// Freeze checkpoints the universe, such that it can be thawed back
// into the same state later. When Freeze returns, the universe is no
// longer running or usable until Thaw is used to resume it.
func (u *Universe) Freeze() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return u.closeErr
	}
	u.closed = true

	for hostname, vm := range u.vms {
		if err := vm.freeze(); err != nil {
			u.closeErr = fmt.Errorf("freezing %q: %v", hostname, err)
			return u.closeErr
		}
	}

	for name, cluster := range u.clusters {
		if err := cluster.freeze(); err != nil {
			u.closeErr = fmt.Errorf("freezing cluster %q: %v", name, err)
			return u.closeErr
		}
	}

	// By now all VMs should have shutdown during their freeze. Kill
	// remaining things.
	u.shutdown()

	cfg := &universeConfig{
		NextPort: u.nextPort,
		NextIPv4: u.nextIPv4,
		NextIPv6: u.nextIPv6,
	}
	bs, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		u.closeErr = fmt.Errorf("marshaling universe config: %v", err)
		return u.closeErr
	}
	if err := ioutil.WriteFile(filepath.Join(u.dir, "universe.json"), bs, 0600); err != nil {
		u.closeErr = fmt.Errorf("writing universe config: %v", err)
		return u.closeErr
	}

	return nil
}

// Wait waits for the universe to end.
func (u *Universe) Wait(ctx context.Context) error {
	select {
	case <-u.ctx.Done():
		return nil
	case <-ctx.Done():
		return errors.New("timeout")
	}
}

func (u *Universe) VM(hostname string) *VM {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.vms[hostname]
}

func (u *Universe) Cluster(name string) *Cluster {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.clusters[name]
}

func (u *Universe) ipv4() net.IP {
	u.mu.Lock()
	defer u.mu.Unlock()

	ret := u.nextIPv4
	u.nextIPv4 = make(net.IP, 4)
	copy(u.nextIPv4, ret)
	u.nextIPv4[3]++
	return ret
}

func (u *Universe) ipv6() net.IP {
	u.mu.Lock()
	defer u.mu.Unlock()

	ret := u.nextIPv6
	u.nextIPv6 = make(net.IP, 16)
	copy(u.nextIPv6, ret)
	u.nextIPv6[15]++
	return ret
}

func (u *Universe) port() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	ret := u.nextPort
	u.nextPort++
	return ret
}
