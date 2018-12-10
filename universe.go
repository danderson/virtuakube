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
	"syscall"
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

// A Universe is a virtual sandbox and its associated resources.
type Universe struct {
	// Root containing all the stuff in the universe.
	dir string
	// VDE switch control socket, used by VMs to attach to the switch.
	switchSock string

	closedCh chan bool

	mu sync.Mutex

	// Network resources for the universe. VMs request these on
	// creation.
	cfg *universeConfig

	// Resources in the universe: a virtual switch, some VMs, some k8s
	// clusters.
	swtch    *exec.Cmd
	images   map[string]*Image
	vms      map[string]*VM
	clusters map[string]*Cluster

	// VMs and clusters that were created during this session. Close
	// will destroy these VMs, Save will persist them.
	newImages   map[string]bool
	newVMs      map[string]bool
	newClusters map[string]bool

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

// Create creates a new empty Universe in dir. The directory must not
// already exist.
func Create(dir string) (*Universe, error) {
	cfg := &universeConfig{
		NextPort: 50000,
		NextIPv4: net.ParseIP("172.20.0.1"),
		NextIPv6: net.ParseIP("fd00::1"),
	}

	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, "image"), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, "vm"), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, "cluster"), 0700); err != nil {
		return nil, err
	}

	u, err := mkUniverse(cfg, dir)
	if err != nil {
		return nil, err
	}
	if err := u.writeUniverseConfig(); err != nil {
		u.Destroy()
		return nil, err
	}

	return u, nil
}

// Open opens the existing Universe in dir, and resumes any VMs and
// clusters within.
func Open(dir string) (*Universe, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	bs, err := ioutil.ReadFile(filepath.Join(dir, "universe.json"))
	if err != nil {
		return nil, err
	}

	var cfg universeConfig
	if err := json.Unmarshal(bs, &cfg); err != nil {
		return nil, err
	}

	u, err := mkUniverse(&cfg, dir)
	if err != nil {
		return nil, err
	}

	// Recreate all images.
	f, err := os.Open(filepath.Join(u.dir, "image"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	imagePaths, err := f.Readdir(0)
	if err != nil {
		return nil, err
	}

	for _, imagePath := range imagePaths {
		u.images[imagePath.Name()] = &Image{
			path: filepath.Join(u.dir, "image", imagePath.Name()),
		}
	}

	// Thaw all VMs.
	f, err = os.Open(filepath.Join(u.dir, "vm"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vmPaths, err := f.Readdir(0)
	if err != nil {
		return nil, err
	}

	// thaw all VMs concurrently. This is the expensive step where
	// they struggle to load their huge memory snapshots. At the end
	// of thaw, they're fully loaded, but with their CPUs stopped.
	res := make(chan error, len(vmPaths))
	for _, vmPath := range vmPaths {
		go func(name string) {
			vm, err := u.thawVM(name)
			if err != nil {
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

	// Now that the expensive load is done, blow through all VMs and
	// restart their CPUs in rapid succession, to keep the clock skew
	// between VMs minimal.
	for _, vmPath := range vmPaths {
		if err := u.VM(vmPath.Name()).boot(); err != nil {
			return nil, err
		}
	}

	// Thaw all cluster objects, now that the cluster VMs are running.
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

func mkUniverse(cfg *universeConfig, dir string) (*Universe, error) {
	if err := checkTools(universeTools); err != nil {
		return nil, err
	}

	sock := filepath.Join(dir, "switch")

	ret := &Universe{
		dir:         dir,
		closedCh:    make(chan bool),
		cfg:         cfg,
		images:      map[string]*Image{},
		vms:         map[string]*VM{},
		clusters:    map[string]*Cluster{},
		newImages:   map[string]bool{},
		newVMs:      map[string]bool{},
		newClusters: map[string]bool{},
		swtch: exec.Command(
			"vde_switch",
			"--sock", sock,
			"-m", "0600",
		),
		switchSock: sock,
	}
	ret.cfg.NextIPv4 = ret.cfg.NextIPv4.To4()
	ret.swtch.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	ret.mu.Lock()
	defer ret.mu.Unlock()

	if err := ret.swtch.Start(); err != nil {
		ret.Close()
		return nil, err
	}
	// Destroy the universe if the virtual switch exits.
	go func() {
		ret.swtch.Wait()
		ret.Close()
	}()

	return ret, nil
}

// Close closes the universe, discarding all changes since the last
// call to Save.
func (u *Universe) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return u.closeErr
	}

	u.closeWithLock()
	close(u.closedCh)
	return u.closeErr
}

func (u *Universe) closeWithLock() {
	// Assumes u hasn't been closed already. Caller's responsibility
	// to check that.
	u.closed = true

	for _, vm := range u.vms {
		if err := vm.Close(); err != nil {
			u.closeErr = err
		}
	}

	u.swtch.Process.Kill()

	for image, destroy := range u.newImages {
		if !destroy {
			continue
		}
		if err := os.Remove(u.images[image].path); err != nil {
			u.closeErr = err
		}
	}
	for hostname, destroy := range u.newVMs {
		if !destroy {
			continue
		}
		if err := os.RemoveAll(u.vms[hostname].dir); err != nil {
			u.closeErr = err
		}
	}
	for name, destroy := range u.newClusters {
		if !destroy {
			continue
		}
		if err := os.RemoveAll(u.clusters[name].dir); err != nil {
			u.closeErr = err
		}
	}
}

// Destroy closes the universe and recursively deletes the universe
// directory.
func (u *Universe) Destroy() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return u.closeErr
	}

	u.closeWithLock()

	if err := os.RemoveAll(u.dir); err != nil {
		u.closeErr = err
	}

	close(u.closedCh)
	return u.closeErr
}

// Save snapshots the current state of VMs and clusters, then closes
// the universe.
func (u *Universe) Save() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return u.closeErr
	}

	// VM saving is slow, so parallelize it.
	errs := make(chan error, len(u.vms))
	for hostname, vm := range u.vms {
		go func(hostname string, vm *VM) {
			if err := vm.freeze(); err != nil {
				errs <- fmt.Errorf("freezing %q: %v", hostname, err)
				return
			}
			errs <- nil
		}(hostname, vm)
	}
	for range u.vms {
		if err := <-errs; err != nil {
			u.closeErr = err
			return u.closeErr
		}
	}

	// Clusters are a json file save, not worth the goroutine
	// overhead.
	for name, cluster := range u.clusters {
		if err := cluster.freeze(); err != nil {
			u.closeErr = fmt.Errorf("freezing cluster %q: %v", name, err)
			return u.closeErr
		}
	}

	// By now all VMs should have shutdown during their freeze. Kill
	// remaining things. But clear all the new* maps so that
	// closeWithLock doesn't delete stuff we just saved.
	u.newImages = nil
	u.newVMs = nil
	u.newClusters = nil
	u.closeWithLock()

	if err := u.writeUniverseConfig(); err != nil {
		u.closeErr = err
		return u.closeErr
	}

	close(u.closedCh)
	return nil
}

func (u *Universe) writeUniverseConfig() error {
	bs, err := json.MarshalIndent(u.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling universe config: %v", err)
	}
	if err := ioutil.WriteFile(filepath.Join(u.dir, "universe.json"), bs, 0600); err != nil {
		return fmt.Errorf("writing universe config: %v", err)
	}
	return nil
}

// Wait waits for the universe to be Closed, Saved or Destroyed.
func (u *Universe) Wait(ctx context.Context) error {
	select {
	case <-u.closedCh:
		return nil
	case <-ctx.Done():
		return errors.New("timeout")
	}
}

// Image returns the named image, or nil if no such image exists in
// the universe.
func (u *Universe) Image(name string) *Image {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.images[name]
}

// VM returns the VM with the given hostname, or nil if no such VM
// exists in the universe.
func (u *Universe) VM(hostname string) *VM {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.vms[hostname]
}

// VM returns a list of all VMs in the universe.
func (u *Universe) VMs() []*VM {
	u.mu.Lock()
	defer u.mu.Unlock()
	ret := make([]*VM, 0, len(u.vms))

	for _, vm := range u.vms {
		ret = append(ret, vm)
	}

	return ret
}

// Cluster returns the Cluster with the given name, or nil if no such
// Cluster exists in the universe.
func (u *Universe) Cluster(name string) *Cluster {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.clusters[name]
}

// Clusters returns a list of all Clusters in the universe.
func (u *Universe) Clusters() []*Cluster {
	u.mu.Lock()
	defer u.mu.Unlock()
	ret := make([]*Cluster, 0, len(u.clusters))

	for _, cluster := range u.clusters {
		ret = append(ret, cluster)
	}

	return ret
}

func (u *Universe) ipv4() net.IP {
	u.mu.Lock()
	defer u.mu.Unlock()

	ret := u.cfg.NextIPv4
	u.cfg.NextIPv4 = make(net.IP, 4)
	copy(u.cfg.NextIPv4, ret)
	u.cfg.NextIPv4[3]++
	return ret
}

func (u *Universe) ipv6() net.IP {
	u.mu.Lock()
	defer u.mu.Unlock()

	ret := u.cfg.NextIPv6
	u.cfg.NextIPv6 = make(net.IP, 16)
	copy(u.cfg.NextIPv6, ret)
	u.cfg.NextIPv6[15]++
	return ret
}

func (u *Universe) port() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	ret := u.cfg.NextPort
	u.cfg.NextPort++
	return ret
}
