package virtuakube

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var incrVMID = make(chan int)

func init() {
	id := 1
	go func() {
		for {
			incrVMID <- id
			id++
		}
	}()
}

// VMConfig is the configuration for a virtual machine.
type VMConfig struct {
	// Image is the path to the base disk image for the VM. By
	// default, the image is treated as read-only and a temporary CoW
	// overlay is used to run the VM.
	Image string
	// NoOverlay specifies that the VM should use Image as its disk
	// image directly. Image will be modified by the running system.
	NoOverlay bool
	// Hostname to set on the VM
	Hostname string
	// Amount of RAM.
	MemoryMiB int
	// Ports to forward from localhost to the VM
	PortForwards map[int]bool
	// If true, the VM terminating doesn't destroy the universe.
	NonEssential bool
	// If non-nil, log commands executed by vm.Run and friends, along
	// with the output of the commands.
	CommandLog io.Writer
	// If true, use pure software emulation without hardware
	// acceleration.
	NoKVM bool

	// Only available to image builder.
	kernelPath string
	initrdPath string
	cmdline    string
}

// Copy returns a deep copy of the VM config.
func (v *VMConfig) Copy() *VMConfig {
	ret := &VMConfig{
		Image:        v.Image,
		NoOverlay:    v.NoOverlay,
		Hostname:     v.Hostname,
		MemoryMiB:    v.MemoryMiB,
		PortForwards: make(map[int]bool),
		NonEssential: v.NonEssential,
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

// VM is a virtual machine.
type VM struct {
	cfg      *VMConfig
	u        *Universe
	diskPath string
	mac      string
	forwards map[int]int
	ipv4     net.IP
	ipv6     net.IP
	ctx      context.Context
	shutdown context.CancelFunc

	cmd   *exec.Cmd
	ssh   *ssh.Client
	ready chan *ssh.Client

	mu      sync.Mutex
	started bool
	closed  bool
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

func validateVMConfig(cfg *VMConfig) (*VMConfig, error) {
	if cfg == nil || cfg.Image == "" {
		return nil, errors.New("VMConfig with at least BackingImagePath is required")
	}

	cfg = cfg.Copy()

	bp, err := filepath.Abs(cfg.Image)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(bp); err != nil {
		return nil, err
	}
	cfg.Image = bp

	if cfg.Hostname == "" {
		cfg.Hostname = randomHostname()
	}
	if cfg.MemoryMiB == 0 {
		cfg.MemoryMiB = 1024
	}

	cfg.PortForwards[22] = true

	return cfg, nil
}

func makeForwards(fwds map[int]int) string {
	var ret []string
	for dst, src := range fwds {
		ret = append(ret, fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:%d", src, dst))
	}
	return strings.Join(ret, ",")
}

// NewVM creates an unstarted VM with the given configuration.
func (u *Universe) NewVM(cfg *VMConfig) (*VM, error) {
	cfg, err := validateVMConfig(cfg)
	if err != nil {
		return nil, err
	}

	if u.VM(cfg.Hostname) != nil {
		return nil, fmt.Errorf("universe already has a VM named %q", cfg.Hostname)
	}

	tmp, err := u.Tmpdir("vm")
	if err != nil {
		return nil, err
	}

	diskPath := cfg.Image
	if !cfg.NoOverlay {
		diskPath = filepath.Join(tmp, "disk.qcow2")
		disk := exec.Command(
			"qemu-img",
			"create",
			"-f", "qcow2",
			"-b", cfg.Image,
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
		fwds[fwd] = <-u.ports
	}

	ctx, cancel := context.WithCancel(u.Context())

	ret := &VM{
		cfg:      cfg,
		u:        u,
		diskPath: diskPath,
		mac:      randomMAC(),
		forwards: fwds,
		ipv4:     u.ipv4(),
		ipv6:     u.ipv6(),
		ctx:      ctx,
		shutdown: cancel,
		ready:    make(chan *ssh.Client),
	}
	ret.cmd = exec.CommandContext(
		ret.ctx,
		"qemu-system-x86_64",
		"-m", strconv.Itoa(ret.cfg.MemoryMiB),
		"-device", "virtio-net,netdev=net0,mac=52:54:00:12:34:56",
		"-device", fmt.Sprintf("virtio-net,netdev=net1,mac=%s", ret.mac),
		"-device", "virtio-rng-pci,rng=rng0",
		"-device", "virtio-serial",
		"-object", "rng-random,filename=/dev/urandom,id=rng0",
		"-netdev", fmt.Sprintf("user,id=net0,%s", makeForwards(ret.forwards)),
		"-netdev", fmt.Sprintf("vde,id=net1,sock=%s", u.switchSock()),
		"-drive", fmt.Sprintf("if=virtio,file=%s,media=disk", ret.diskPath),
		"-nographic",
		"-serial", "null",
		"-monitor", "none",
	)
	if !cfg.NoKVM {
		ret.cmd.Args = append(ret.cmd.Args, "-enable-kvm")
	}
	if cfg.kernelPath != "" {
		ret.cmd.Args = append(ret.cmd.Args, "-kernel", cfg.kernelPath, "-initrd", cfg.initrdPath, "-append", cfg.cmdline)
	}

	u.vms[cfg.Hostname] = ret

	return ret, nil
}

// Start boots the virtual machine. The universe is destroyed if the
// VM ever shuts down.
func (v *VM) Start() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.started {
		return errors.New("already started")
	}
	v.started = true

	v.cmd.Stdout = os.Stdout
	v.cmd.Stderr = os.Stderr
	if err := v.cmd.Start(); err != nil {
		v.Close()
		return err
	}

	go func() {
		v.cmd.Wait()
		// TODO: better logging and stuff
		v.Close()
	}()

	// Try dialing SSH
	for v.ctx.Err() == nil {
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

	err := v.RunMultiple(
		"hostnamectl set-hostname "+v.cfg.Hostname,
		fmt.Sprintf("ip addr add %s/24 dev ens4", v.ipv4),
		fmt.Sprintf("ip addr add %s/24 dev ens4", v.ipv6),
		"ip link set dev ens4 up",
	)
	if err != nil {
		v.Close()
		return err
	}

	return nil
}

func (v *VM) Run(command string) ([]byte, error) {
	sess, err := v.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &out
	if v.cfg.CommandLog != nil {
		sess.Stdout = io.MultiWriter(&out, v.cfg.CommandLog)
		sess.Stderr = sess.Stdout
		fmt.Fprintln(v.cfg.CommandLog, "+ "+command)
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
	sess, err := v.ssh.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = bytes.NewBuffer(bs)
	if v.cfg.CommandLog != nil {
		fmt.Fprintf(v.cfg.CommandLog, "+ (write file %s)\n", path)
	}

	return sess.Run("cat >" + path)
}

func (v *VM) ReadFile(path string) ([]byte, error) {
	sess, err := v.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	if v.cfg.CommandLog != nil {
		fmt.Fprintf(v.cfg.CommandLog, "+ (read file %s)\n", path)
	}
	return sess.Output("cat " + path)
}

func (v *VM) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true

	v.shutdown()

	if !v.cfg.NonEssential {
		v.u.Close()
	}

	return nil
}

// ForwardedPort returns the port on localhost that maps to the given
// port on the VM.
func (v *VM) ForwardedPort(dst int) int {
	return v.forwards[dst]
}

func (v *VM) IPv4() net.IP { return v.ipv4 }
func (v *VM) IPv6() net.IP { return v.ipv6 }
