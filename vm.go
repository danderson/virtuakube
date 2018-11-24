package virtuakube

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type VMConfig struct {
	// BackingImagePath is the base disk image for the VM.
	BackingImagePath string
	// Hostname to offer the VM over DHCP
	// TODO: does nothing yet
	Hostname string
	// Amount of RAM.
	MemoryMiB int
	// If true, create a window for the VM's display.
	Display bool
	// Ports to forward from localhost to the VM
	PortForwards []int
	// BootScript is the path to a boot script that the VM should
	// execute during boot.
	BootScript string
}

type VM struct {
	cfg      *VMConfig
	u        *Universe
	hostPath string
	diskPath string
	mac      string
	forwards map[int]int
	ipv4     net.IP
	ipv6     net.IP
	cmd      *exec.Cmd

	closedMu sync.Mutex
	closed   bool
}

func randomMAC() string {
	mac := make(net.HardwareAddr, 6)
	if _, err := rand.Read(mac); err != nil {
		panic("system ran out of randomness")
	}
	mac[0] = 0x52
	return mac.String()
}

func validateVMConfig(cfg *VMConfig) error {
	if cfg == nil || cfg.BackingImagePath == "" {
		return errors.New("VMConfig with at least BackingImagePath is required")
	}

	bp, err := filepath.Abs(cfg.BackingImagePath)
	if err != nil {
		return err
	}
	if _, err = os.Stat(bp); err != nil {
		return err
	}
	cfg.BackingImagePath = bp

	if cfg.BootScript != "" {
		bp, err = filepath.Abs(cfg.BootScript)
		if err != nil {
			return err
		}
		if _, err = os.Stat(bp); err != nil {
			return err
		}
		cfg.BootScript = bp
	}

	if cfg.Hostname == "" {
		cfg.Hostname = "vm"
	}
	if cfg.MemoryMiB == 0 {
		cfg.MemoryMiB = 1024
	}

	return nil
}

func makeForwards(fwds map[int]int) string {
	var ret []string
	for dst, src := range fwds {
		ret = append(ret, fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:%d", src, dst))
	}
	return strings.Join(ret, ",")
}

func (u *Universe) NewVM(cfg *VMConfig) (*VM, error) {
	if err := validateVMConfig(cfg); err != nil {
		return nil, err
	}

	tmp, err := u.Tmpdir("vm")
	if err != nil {
		return nil, err
	}

	hostPath := filepath.Join(tmp, "hostfs")
	if err = os.Mkdir(hostPath, 0700); err != nil {
		return nil, err
	}
	diskPath := filepath.Join(tmp, "disk.qcow2")
	disk := exec.Command(
		"qemu-img",
		"create",
		"-f", "qcow2",
		"-b", cfg.BackingImagePath,
		"-f", "qcow2",
		diskPath,
	)
	out, err := disk.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("creating VM disk: %v\n%s", err, string(out))
	}
	fwds := map[int]int{}
	for _, fwd := range cfg.PortForwards {
		fwds[fwd] = <-u.ports
	}

	ret := &VM{
		cfg:      cfg,
		u:        u,
		hostPath: hostPath,
		diskPath: diskPath,
		mac:      randomMAC(),
		forwards: fwds,
		ipv4:     <-u.ipv4s,
		ipv6:     <-u.ipv6s,
	}
	ret.cmd = exec.CommandContext(
		u.Context(),
		"qemu-system-x86_64",
		"-enable-kvm",
		"-m", strconv.Itoa(ret.cfg.MemoryMiB),
		"-device", "virtio-net,netdev=net0,mac=52:54:00:12:34:56",
		"-device", fmt.Sprintf("virtio-net,netdev=net1,mac=%s", ret.mac),
		"-device", "virtio-rng-pci,rng=rng0",
		"-device", "virtio-serial",
		"-object", "rng-random,filename=/dev/urandom,id=rng0",
		"-netdev", fmt.Sprintf("user,id=net0,hostname=%s,%s", ret.cfg.Hostname, makeForwards(ret.forwards)),
		"-netdev", fmt.Sprintf("vde,id=net1,sock=%s", u.switchSock()),
		"-drive", fmt.Sprintf("if=virtio,file=%s,media=disk", ret.diskPath),
		"-virtfs", fmt.Sprintf("local,path=%s,mount_tag=host0,security_model=none,id=host0", ret.hostPath),
	)
	if !cfg.Display {
		ret.cmd.Args = append(ret.cmd.Args,
			"-nographic",
			"-serial", "null",
			"-monitor", "none",
		)
	}

	return ret, nil
}

// Dir returns the path to the directory that is shared with the
// running VM.
func (v *VM) Dir() string {
	return v.hostPath
}

// Start boots the virtual machine. The universe is destroyed if the
// VM ever shuts down.
func (v *VM) Start() error {
	ips := []string{v.ipv4.String(), v.ipv6.String()}
	if err := ioutil.WriteFile(filepath.Join(v.Dir(), "ip"), []byte(strings.Join(ips, "\n")), 0644); err != nil {
		return err
	}

	if v.cfg.BootScript != "" {
		bs, err := ioutil.ReadFile(v.cfg.BootScript)
		if err != nil {
			return err
		}
		if err := ioutil.WriteFile(filepath.Join(v.Dir(), "bootscript.sh"), bs, 0755); err != nil {
			return err
		}
	}

	if err := v.cmd.Start(); err != nil {
		v.u.Close()
		return err
	}

	// Destroy the universe if the VM exits.
	go func() {
		v.cmd.Wait()
		// TODO: better logging and stuff
		v.u.Close()
	}()

	return nil
}

// WaitReady waits until the VM's bootscript creates the boot-done
// file in the shared host directory.
func (v *VM) WaitReady(ctx context.Context) error {
	stop := ctx.Done()
	for {
		select {
		case <-stop:
			return errors.New("timeout")
		default:
		}

		_, err := os.Stat(filepath.Join(v.Dir(), "boot-done"))
		if err != nil {
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		return nil
	}
}

func (v *VM) ForwardedPort(dst int) int {
	return v.forwards[dst]
}
