package virtuakube

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// VMConfig is the configuration for a virtual machine.
type VMConfig struct {
	ImageName    string
	Hostname     string
	MemoryMiB    int
	PortForwards map[int]bool
	CommandLog   io.Writer
	NoKVM        bool

	// Only available to image builder.
	kernelPath string
	initrdPath string
	cmdline    string
}

// Copy returns a deep copy of the VM config.
func (v *VMConfig) Copy() *VMConfig {
	ret := &VMConfig{
		ImageName:    v.ImageName,
		Hostname:     v.Hostname,
		MemoryMiB:    v.MemoryMiB,
		PortForwards: make(map[int]bool),
		CommandLog:   v.CommandLog,
		NoKVM:        v.NoKVM,
		kernelPath:   v.kernelPath,
		initrdPath:   v.initrdPath,
		cmdline:      v.cmdline,
	}
	for fwd, v := range v.PortForwards {
		ret.PortForwards[fwd] = v
	}
	return ret
}

type vmFreezeConfig struct {
	Config       *VMConfig
	MAC          string
	PortForwards map[int]int
	IPv4         net.IP
	IPv6         net.IP
}

// VM is a virtual machine.
type VM struct {
	cfg *vmFreezeConfig

	dir string

	// Closed when the VM has exited.
	stopped chan bool

	mu sync.Mutex

	// Qemu subprocess that runs the VM.
	cmd *exec.Cmd

	// Link to the qemu monitor. Used only to handle freezing.
	monIn  io.WriteCloser
	monOut io.ReadCloser

	// SSH connection to the VM.
	ssh *ssh.Client

	// Tracking the state of the VM to enable/disable parts of the
	// API.
	started bool
	closed  bool
}

func validateVMConfig(cfg *VMConfig) (*VMConfig, error) {
	if cfg == nil || cfg.ImageName == "" {
		return nil, errors.New("no ImageName in VMConfig")
	}

	cfg = cfg.Copy()

	if cfg.Hostname == "" {
		cfg.Hostname = randomHostname()
	}
	if cfg.MemoryMiB == 0 {
		cfg.MemoryMiB = 1024
	}

	cfg.PortForwards[22] = true

	return cfg, nil
}

func (u *Universe) mkVM(cfg *vmFreezeConfig, dir, diskPath string, resume bool) (*VM, error) {
	ret := &VM{
		cfg:     cfg,
		stopped: make(chan bool),
		dir:     dir,
	}
	ret.cmd = exec.Command(
		"qemu-system-x86_64",
		"-m", strconv.Itoa(cfg.Config.MemoryMiB),
		"-device", "virtio-net,netdev=net0,mac=52:54:00:12:34:56",
		"-device", fmt.Sprintf("virtio-net,netdev=net1,mac=%s", cfg.MAC),
		"-device", "virtio-rng-pci,rng=rng0",
		"-device", "virtio-serial",
		"-object", "rng-random,filename=/dev/urandom,id=rng0",
		"-netdev", fmt.Sprintf("user,id=net0,%s", makeForwards(cfg.PortForwards)),
		"-netdev", fmt.Sprintf("vde,id=net1,sock=%s", u.switchSock),
		"-drive", fmt.Sprintf("if=virtio,file=%s,media=disk", diskPath),
		"-nographic",
		"-serial", "null",
		"-monitor", "stdio",
		"-S",
	)
	if !cfg.Config.NoKVM {
		ret.cmd.Args = append(ret.cmd.Args, "-enable-kvm")
	}
	if cfg.Config.kernelPath != "" {
		ret.cmd.Args = append(ret.cmd.Args,
			"-kernel", cfg.Config.kernelPath,
			"-initrd", cfg.Config.initrdPath,
			"-append", cfg.Config.cmdline,
		)
	}
	if resume {
		ret.cmd.Args = append(ret.cmd.Args, "-loadvm", "snap")
	}
	ret.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	ret.cmd.Stderr = os.Stderr

	monIn, err := ret.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %v", err)
	}
	ret.monIn = monIn
	monOut, err := ret.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %v", err)
	}
	ret.monOut = monOut

	if err := ret.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting VM: %v", err)
	}
	go func() {
		ret.cmd.Wait()
		close(ret.stopped)
	}()

	if _, err := readToPrompt(ret.monOut); err != nil {
		ret.Close()
		return nil, fmt.Errorf("reading qemu monitor prompt: %v", err)
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.vms[cfg.Config.Hostname] != nil {
		ret.closeWithLock()
		return nil, fmt.Errorf("universe already has a VM named %q", cfg.Config.Hostname)
	}
	u.vms[cfg.Config.Hostname] = ret
	u.newVMs[cfg.Config.Hostname] = !resume

	return ret, nil
}

// NewVM creates an unstarted VM with the given configuration.
func (u *Universe) NewVM(cfg *VMConfig) (*VM, error) {
	cfg, err := validateVMConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("validating VM config: %v", err)
	}

	dir := filepath.Join(u.dir, "vm", cfg.Hostname)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("creating VM state dir: %v", err)
	}

	diskPath := cfg.ImageName
	if cfg.kernelPath == "" {
		img := u.Image(cfg.ImageName)
		if img == nil {
			return nil, fmt.Errorf("no such image %q", cfg.ImageName)
		}

		diskPath = filepath.Join(dir, "disk.qcow2")

		disk := exec.Command(
			"qemu-img",
			"create",
			"-f", "qcow2",
			"-b", img.path,
			"-f", "qcow2",
			diskPath,
		)
		out, err := disk.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("creating VM disk: %v\n%s", err, string(out))
		}
	}

	wantPorts := []int{}
	for fwd := range cfg.PortForwards {
		wantPorts = append(wantPorts, fwd)
	}
	sort.Ints(wantPorts)
	fwds := map[int]int{}
	for _, fwd := range wantPorts {
		fwds[fwd] = u.port()
	}

	fcfg := &vmFreezeConfig{
		Config:       cfg,
		MAC:          randomMAC(),
		PortForwards: fwds,
		IPv4:         u.ipv4(),
		IPv6:         u.ipv6(),
	}

	vm, err := u.mkVM(fcfg, dir, diskPath, false)
	if err != nil {
		return nil, fmt.Errorf("creating VM: %v", err)
	}

	if err := vm.writeVMConfig(); err != nil {
		vm.Close()
		return nil, fmt.Errorf("writing VM config: %v", err)
	}

	return vm, nil
}

func (v *VM) writeVMConfig() error {
	bs, err := json.MarshalIndent(v.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling frozen VM config: %v", err)
	}
	cfgPath := filepath.Join(v.dir, "config.json")
	if err := ioutil.WriteFile(cfgPath, bs, 0600); err != nil {
		return fmt.Errorf("writing frozen VM config: %v", err)
	}
	return nil
}

func (u *Universe) thawVM(hostname string) (*VM, error) {
	dir := filepath.Join(u.dir, "vm", hostname)
	cfgPath := filepath.Join(dir, "config.json")
	bs, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var cfg vmFreezeConfig
	if err := json.Unmarshal(bs, &cfg); err != nil {
		return nil, err
	}

	diskPath := filepath.Join(dir, "disk.qcow2")
	return u.mkVM(&cfg, dir, diskPath, true)
}

// Start boots the virtual machine.
func (v *VM) Start() error {
	if err := v.boot(); err != nil {
		return err
	}

	err := v.RunMultiple(
		"hostnamectl set-hostname "+v.cfg.Config.Hostname,
		fmt.Sprintf("ip addr add %s/24 dev ens4", v.cfg.IPv4),
		fmt.Sprintf("ip addr add %s/24 dev ens4", v.cfg.IPv6),
		"ip link set dev ens4 up",
	)
	if err != nil {
		v.Close()
		return err
	}

	return nil
}

// boot starts the VM process and waits for SSH to establish.
func (v *VM) boot() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.started {
		return errors.New("already started")
	}

	if _, err := fmt.Fprintf(v.monIn, "cont\n"); err != nil {
		v.closeWithLock()
		return err
	}
	if _, err := readToPrompt(v.monOut); err != nil {
		v.closeWithLock()
		return err
	}

	// Try dialing SSH
	for {
		select {
		case <-v.stopped:
			return errors.New("timeout")
		default:
		}

		sshCfg := &ssh.ClientConfig{
			User:            "root",
			Auth:            []ssh.AuthMethod{ssh.Password("root")},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         time.Second,
		}

		client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", v.ForwardedPort(22)), sshCfg)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		v.ssh = client
		break
	}

	return nil
}

// Wait waits for the VM to shut down.
func (v *VM) Wait(ctx context.Context) error {
	select {
	case <-v.stopped:
		return nil
	case <-ctx.Done():
		return errors.New("timeout")
	}
}

func (v *VM) Run(command string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	sess, err := v.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &out
	if v.cfg.Config.CommandLog != nil {
		sess.Stdout = io.MultiWriter(&out, v.cfg.Config.CommandLog)
		sess.Stderr = sess.Stdout
		fmt.Fprintln(v.cfg.Config.CommandLog, "+ "+command)
	}

	if err := sess.Run(command); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (v *VM) RunMultiple(commands ...string) error {
	for _, cmd := range commands {
		if _, err := v.Run(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (v *VM) WriteFile(path string, bs []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	sess, err := v.ssh.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = bytes.NewBuffer(bs)
	if v.cfg.Config.CommandLog != nil {
		fmt.Fprintf(v.cfg.Config.CommandLog, "+ (write file %s)\n", path)
	}

	return sess.Run("cat >" + path)
}

func (v *VM) ReadFile(path string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	sess, err := v.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	if v.cfg.Config.CommandLog != nil {
		fmt.Fprintf(v.cfg.Config.CommandLog, "+ (read file %s)\n", path)
	}
	return sess.Output("cat " + path)
}

func (v *VM) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.closeWithLock()
}

func (v *VM) closeWithLock() error {
	if v.closed {
		return nil
	}
	v.closed = true
	v.cmd.Process.Kill()
	<-v.stopped
	return nil
}

func (v *VM) freeze() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return errors.New("cannot freeze closed VM")
	}
	v.closed = true

	// Stop the VM CPUs, so we're not competing with the VM while
	// snapshotting.
	if _, err := fmt.Fprintf(v.monIn, "stop\n"); err != nil {
		return err
	}
	if _, err := readToPrompt(v.monOut); err != nil {
		return err
	}

	// Write the snapshot.
	if _, err := fmt.Fprintf(v.monIn, "savevm snap\n"); err != nil {
		return err
	}
	if _, err := readToPrompt(v.monOut); err != nil {
		return err
	}

	// Shut down qemu. We don't expect a monitor response here,
	// instead we wait for the context to get canceled, which will get
	// triggered by the goroutine that's waiting for the qemu process
	// to exit.
	if _, err := fmt.Fprintf(v.monIn, "quit\n"); err != nil {
		return err
	}
	<-v.stopped

	// Clear CommandLog, since we can't persist that, and we just
	// stopped the VM anyway.
	v.cfg.Config.CommandLog = nil

	// Save the VM config
	return v.writeVMConfig()
}

func (v *VM) Hostname() string {
	return v.cfg.Config.Hostname
}

// ForwardedPort returns the port on localhost that maps to the given
// port on the VM.
func (v *VM) ForwardedPort(dst int) int {
	return v.cfg.PortForwards[dst]
}

func (v *VM) IPv4() net.IP { return v.cfg.IPv4 }
func (v *VM) IPv6() net.IP { return v.cfg.IPv6 }

var (
	qemuPrompt = []byte("\r\n(qemu) ")
	ansiCSI_K  = []byte("\x1b[K")
)

// Read to the next (qemu) prompt, and return whatever was before
// that.
func readToPrompt(r io.Reader) (string, error) {
	var buf bytes.Buffer
	b := make([]byte, 100)
	for {
		n, err := r.Read(b)
		if err != nil {
			return "", err
		}
		buf.Write(b[:n])
		have := buf.Bytes()
		if bytes.HasSuffix(have, qemuPrompt) {
			buf.Reset()
			ret := bytes.TrimSuffix(have, qemuPrompt)
			if i := bytes.LastIndex(ret, ansiCSI_K); i != -1 {
				ret = ret[i+len(ansiCSI_K):]
			}
			return strings.TrimSpace(strings.Replace(string(ret), "\r\n", "\n", -1)), nil
		}
	}
}

func randomMAC() string {
	mac := make(net.HardwareAddr, 6)
	if _, err := rand.Read(mac); err != nil {
		panic("system ran out of randomness")
	}
	// Sets the MAC to be one of the "private" range. Private MACs
	// have the second-least significant bit of the most significant
	// byte set.
	mac[0] = 0x52
	return mac.String()
}

func randomHostname() string {
	rnd := make([]byte, 6)
	if _, err := rand.Read(rnd); err != nil {
		panic("system ran out of randomness")
	}
	return fmt.Sprintf("vm%x", rnd)
}

// Make a series of "hostfwd" statements for the qemu commandline.
func makeForwards(fwds map[int]int) string {
	var ret []string
	for dst, src := range fwds {
		ret = append(ret, fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:%d", src, dst))
	}
	return strings.Join(ret, ",")
}
