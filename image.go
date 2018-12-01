package virtuakube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"go.universe.tf/virtuakube/internal/assets"
)

var buildTools = []string{
	"qemu-system-x86_64",
	"qemu-img",
	"docker",
	"virt-make-fs",
}

const (
	dockerfile = `
FROM debian:buster
RUN apt-get -y update
RUN DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends \
  dbus \
  ifupdown \
  isc-dhcp-client \
  isc-dhcp-common \
  linux-image-amd64 \
  openssh-server \
  systemd-sysv
RUN echo "root:root" | chpasswd
RUN echo "PermitRootLogin yes" >>/etc/ssh/sshd_config
RUN echo "auto ens3\niface ens3 inet dhcp" >/etc/network/interfaces
RUN echo "supersede domain-name-servers 8.8.8.8;" >>/etc/dhcp/dhclient.conf
`

	setupScript = `
set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get -y upgrade --no-install-recommends
apt-get -y install --no-install-recommends ca-certificates grub2

cat >/etc/hosts <<EOF
127.0.0.1 localhost
::1 localhost
EOF

cat >/etc/fstab <<EOF
/dev/vda1 / ext4 rw,relatime 0 1
EOF

update-initramfs -u

grub-install /dev/vda
perl -pi -e 's/GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
update-grub2

rm /etc/machine-id /var/lib/dbus/machine-id
touch /etc/machine-id
chattr +i /etc/machine-id
poweroff
`
)

type BuildConfig struct {
	OutputPath string
	TempDir    string
	BuildLog   io.Writer
	NoKVM      bool
}

func BuildBaseImage(ctx context.Context, cfg *BuildConfig) error {
	if err := checkTools(buildTools); err != nil {
		return err
	}

	tmp, err := ioutil.TempDir(cfg.TempDir, "virtuakube-build")
	if err != nil {
		return fmt.Errorf("creating tempdir in %q: %v", cfg.TempDir, err)
	}
	defer os.RemoveAll(tmp)

	if err := ioutil.WriteFile(filepath.Join(tmp, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return fmt.Errorf("writing dockerfile: %v", err)
	}

	iidPath := filepath.Join(tmp, "iid")
	cmd := exec.CommandContext(ctx, "docker", "build", "--iidfile", iidPath, tmp)
	cmd.Stdout = cfg.BuildLog
	cmd.Stderr = cfg.BuildLog
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running docker build: %v", err)
	}

	iid, err := ioutil.ReadFile(iidPath)
	if err != nil {
		return fmt.Errorf("reading image ID: %v", err)
	}

	cidPath := filepath.Join(tmp, "cid")
	cmd = exec.CommandContext(
		ctx,
		"docker", "run",
		"--cidfile", cidPath,
		fmt.Sprintf("--mount=type=bind,source=%s,destination=/tmp/ctx", tmp),
		string(iid),
		"cp", "/vmlinuz", "/initrd.img", "/tmp/ctx",
	)
	cmd.Stdout = cfg.BuildLog
	cmd.Stderr = cfg.BuildLog
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extracting kernel from container: %v", err)
	}

	cid, err := ioutil.ReadFile(cidPath)
	if err != nil {
		return fmt.Errorf("reading image ID: %v", err)
	}

	tarPath := filepath.Join(tmp, "fs.tar")
	cmd = exec.CommandContext(
		ctx,
		"docker", "export",
		"-o", tarPath,
		string(cid),
	)
	cmd.Stdout = cfg.BuildLog
	cmd.Stderr = cfg.BuildLog
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exporting image tarball: %v", err)
	}

	imgPath := filepath.Join(tmp, "fs.img")
	cmd = exec.CommandContext(
		ctx,
		"virt-make-fs",
		"--partition", "--format=qcow2",
		"--type=ext4", "--size=10G",
		tarPath, imgPath,
	)
	cmd.Stdout = cfg.BuildLog
	cmd.Stderr = cfg.BuildLog
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("creating image file: %v", err)
	}

	if err := os.Remove(tarPath); err != nil {
		return fmt.Errorf("removing image tarball: %v", err)
	}

	vmCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd = exec.CommandContext(
		vmCtx,
		"qemu-system-x86_64",
		"-m", "2048",
		"-device", "virtio-net,netdev=net0,mac=52:54:00:12:34:56",
		"-device", "virtio-rng-pci,rng=rng0",
		"-object", "rng-random,filename=/dev/urandom,id=rng0",
		"-netdev", "user,id=net0,hostfwd=tcp:127.0.0.1:50000-:22",
		"-drive", fmt.Sprintf("if=virtio,file=%s,media=disk", imgPath),
		"-kernel", filepath.Join(tmp, "vmlinuz"),
		"-initrd", filepath.Join(tmp, "initrd.img"),
		"-append", "root=/dev/vda1 rw",
		"-nographic",
		"-serial", "null",
		"-monitor", "none",
	)
	if !cfg.NoKVM {
		cmd.Args = append(cmd.Args, "-enable-kvm")
	}
	cmd.Stdout = cfg.BuildLog
	cmd.Stderr = cfg.BuildLog
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting build VM: %v", err)
	}
	go func() {
		cmd.Wait()
		cancel()
	}()

	client, err := dialSSH(vmCtx, "127.0.0.1:50000")
	if err != nil {
		return fmt.Errorf("connecting to VM with SSH: %v", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("creating SSH session: %v", err)
	}
	defer sess.Close()
	sess.Stdout = cfg.BuildLog
	sess.Stderr = cfg.BuildLog
	sess.Stdin = bytes.NewBuffer([]byte(setupScript))
	if err := sess.Run("cat >/tmp/install.sh"); err != nil {
		return fmt.Errorf("copying setup script: %v", err)
	}

	sess, err = client.NewSession()
	if err != nil {
		return fmt.Errorf("creating SSH session: %v", err)
	}
	defer sess.Close()
	sess.Stdout = cfg.BuildLog
	sess.Stderr = cfg.BuildLog
	if err := sess.Run("bash /tmp/install.sh"); err != nil {
		return fmt.Errorf("running setup script: %v", err)
	}

	// Wait for qemu to terminate, since the script invoked `poweroff`.
	<-vmCtx.Done()

	cmd = exec.CommandContext(
		ctx,
		"qemu-img", "convert",
		"-O", "qcow2",
		imgPath, cfg.OutputPath,
	)
	cmd.Stdout = cfg.BuildLog
	cmd.Stderr = cfg.BuildLog
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running qemu-img convert: %v", err)
	}

	return nil
}

func BuildK8sImage(ctx context.Context, cfg *BuildConfig) error {
	if err := checkTools(buildTools); err != nil {
		return err
	}

	if err := BuildBaseImage(ctx, cfg); err != nil {
		return err
	}

	u, err := New(ctx)
	if err != nil {
		return err
	}
	defer u.Close()

	v, err := u.NewVM(&VMConfig{
		Image:      cfg.OutputPath,
		NoOverlay:  true,
		MemoryMiB:  2048,
		CommandLog: cfg.BuildLog,
	})
	if err != nil {
		return err
	}
	defer v.Close()

	if err := v.Start(); err != nil {
		return err
	}

	repos := []byte(`
deb [arch=amd64] https://download.docker.com/linux/debian buster stable
deb http://apt.kubernetes.io/ kubernetes-xenial main
`)
	if err := v.WriteFile("/etc/apt/sources.list.d/k8s.list", repos); err != nil {
		return err
	}

	pkgs := []string{
		"curl",
		"ebtables",
		"ethtool",
		"gpg",
		"gpg-agent",
	}
	k8sPkgs := []string{
		"docker-ce=18.06.*",
		"kubelet",
		"kubeadm",
		"kubectl",
	}
	err = v.RunMultiple(
		"DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends "+strings.Join(pkgs, " "),
		"curl -fsSL https://download.docker.com/linux/debian/gpg | apt-key add -",
		"curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -",
		"apt-get -y update",
		"DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends "+strings.Join(k8sPkgs, " "),
		"echo br_netfilter >>/etc/modules",
		"echo export KUBECONFIG=/etc/kubernetes/admin.conf >>/etc/profile.d/k8s.sh",
		"systemctl start docker",
		"kubeadm config images pull",
	)
	if err != nil {
		return err
	}

	imgs := strings.Split(string(assets.MustAsset("addon-images")), " ")
	for _, img := range imgs {
		if img == "" {
			continue
		}
		if _, err := v.Run("docker pull " + img); err != nil {
			return err
		}
	}

	err = v.RunMultiple(
		"sync",
		"poweroff",
	)
	if err != nil {
		return err
	}

	return nil
}

func dialSSH(ctx context.Context, target string) (*ssh.Client, error) {
	sshCfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password("root")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Second,
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	for ctx.Err() == nil {
		client, err := ssh.Dial("tcp", target, sshCfg)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		return client, nil
	}

	return nil, ctx.Err()
}
