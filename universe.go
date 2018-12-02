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
	tmpdir   string
	ctx      context.Context
	shutdown context.CancelFunc
	nextPort int
	nextIP4  net.IP
	nextIP6  net.IP
	vms      map[string]*VM
	clusters map[string]*Cluster

	swtch *exec.Cmd
	sock  string

	closeMu  sync.Mutex
	closed   bool
	closeErr error
}

// New creates a new virtual universe. The ctx controls the overall
// lifetime of the universe, i.e. if the context is canceled or times
// out, the universe will be destroyed.
func New(ctx context.Context, tmpdir string) (*Universe, error) {
	cfg := &universeFreezeConfig{
		NextPort: 50000,
		NextIPv4: net.ParseIP("172.20.0.1"),
		NextIPv6: net.ParseIP("fd00::1"),
	}
	return mkUniverse(ctx, cfg, tmpdir)
}

func Thaw(ctx context.Context, freezeDir, tmpdir string) (*Universe, error) {
	bs, err := ioutil.ReadFile(filepath.Join(freezeDir, "_universe.json"))
	if err != nil {
		return nil, err
	}

	var cfg universeFreezeConfig
	if err := json.Unmarshal(bs, &cfg); err != nil {
		return nil, err
	}

	u, err := mkUniverse(ctx, &cfg, tmpdir)
	if err != nil {
		return nil, err
	}

	for _, hostname := range cfg.VMs {
		if _, err := u.thawVM(freezeDir, hostname); err != nil {
			return nil, err
		}
	}
	for hostname, vm := range u.vms {
		if err := vm.boot(); err != nil {
			return nil, fmt.Errorf("booting VM %q: %v", hostname, err)
		}
	}
	for hostname, vm := range u.vms {
		if err := vm.waitReady(); err != nil {
			return nil, fmt.Errorf("waiting for VM %q: %v", hostname, err)
		}
	}

	for _, name := range cfg.Clusters {
		if _, err := u.thawCluster(freezeDir, name); err != nil {
			return nil, fmt.Errorf("thawing cluster %q: %v", name, err)
		}
	}

	return u, nil
}

func mkUniverse(ctx context.Context, cfg *universeFreezeConfig, tmpdir string) (*Universe, error) {
	if err := checkTools(universeTools); err != nil {
		return nil, err
	}

	p, err := ioutil.TempDir(tmpdir, "virtuakube")
	if err != nil {
		return nil, err
	}

	ctx, shutdown := context.WithCancel(ctx)

	sock := filepath.Join(p, "switch")

	ret := &Universe{
		tmpdir:   p,
		ctx:      ctx,
		shutdown: shutdown,
		nextPort: cfg.NextPort,
		nextIP4:  cfg.NextIPv4.To4(),
		nextIP6:  cfg.NextIPv6,
		vms:      map[string]*VM{},
		clusters: map[string]*Cluster{},
		swtch: exec.CommandContext(
			ctx,
			"vde_switch",
			"--sock", sock,
			"-m", "0600",
		),
		sock: sock,
	}

	if err := ret.swtch.Start(); err != nil {
		ret.Close()
		return nil, err
	}
	// Destroy the universe if the virtual switch exits
	go func() {
		ret.swtch.Wait()
		// TODO: logging and stuff
		ret.Close()
	}()
	// Destroy the universe if the parent context cancels
	go func() {
		<-ctx.Done()
		ret.Close()
	}()

	return ret, nil
}

// Tmpdir creates a temporary directory and returns its absolute
// path. The directory will be cleaned up when the universe is
// destroyed.
func (u *Universe) Tmpdir(prefix string) (string, error) {
	p, err := ioutil.TempDir(u.tmpdir, prefix)
	if err != nil {
		return "", err
	}
	return p, nil
}

// Context returns a context that gets canceled when the universe is
// destroyed.
func (u *Universe) Context() context.Context {
	return u.ctx
}

// Close destroys the universe, freeing up processes and temporary
// files.
func (u *Universe) Close() error {
	u.closeMu.Lock()
	defer u.closeMu.Unlock()
	return u.closeWithLock()
}

func (u *Universe) closeWithLock() error {
	if u.closed {
		return u.closeErr
	}
	u.closed = true

	u.shutdown()

	u.closeErr = os.RemoveAll(u.tmpdir)
	return u.closeErr
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

type universeFreezeConfig struct {
	NextPort int
	NextIPv4 net.IP
	NextIPv6 net.IP
	VMs      []string
	Clusters []string
}

// Freeze checkpoints the universe into freezeDir, such that it can be
// thawed back into the same state later.
func (u *Universe) Freeze(freezeDir string) error {
	// Grab the universe lock to prevent Close from interfering in the
	// freeze.
	u.closeMu.Lock()
	defer u.closeMu.Unlock()

	for hostname, vm := range u.vms {
		if err := vm.pause(); err != nil {
			return fmt.Errorf("pausing %q: %v", hostname, err)
		}
	}

	for hostname, vm := range u.vms {
		if err := vm.freeze(freezeDir); err != nil {
			return fmt.Errorf("freezing VM %q: %v", hostname, err)
		}
	}

	for name, cluster := range u.clusters {
		if err := cluster.freeze(freezeDir); err != nil {
			return fmt.Errorf("freezing cluster %q: %v", name, err)
		}
	}

	cfg := &universeFreezeConfig{
		NextPort: u.nextPort,
		NextIPv4: u.nextIP4,
		NextIPv6: u.nextIP6,
	}
	for hostname := range u.vms {
		cfg.VMs = append(cfg.VMs, hostname)
	}
	for name := range u.clusters {
		cfg.Clusters = append(cfg.Clusters, name)
	}

	bs, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling universe config: %v", err)
	}
	if err := ioutil.WriteFile(filepath.Join(freezeDir, "_universe.json"), bs, 0600); err != nil {
		return fmt.Errorf("writing universe config: %v", err)
	}

	return u.closeWithLock()
}

func (u *Universe) VM(hostname string) *VM {
	return u.vms[hostname]
}

func (u *Universe) Cluster(name string) *Cluster {
	return u.clusters[name]
}

func (u *Universe) switchSock() string {
	return u.sock
}

func (u *Universe) ipv4() net.IP {
	ret := u.nextIP4
	u.nextIP4 = make(net.IP, 4)
	copy(u.nextIP4, ret)
	u.nextIP4[3]++
	return ret
}

func (u *Universe) ipv6() net.IP {
	ret := u.nextIP6
	u.nextIP6 = make(net.IP, 16)
	copy(u.nextIP6, ret)
	u.nextIP6[15]++
	return ret
}

func (u *Universe) port() int {
	ret := u.nextPort
	u.nextPort++
	return ret
}
