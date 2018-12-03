package virtuakube

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
FROM debian:stretch
RUN apt-get -y update
RUN DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends \
  ca-certificates \
  dbus \
  grub2 \
  ifupdown \
  isc-dhcp-client \
  isc-dhcp-common \
  linux-image-amd64 \
  openssh-server \
  systemd-sysv
RUN DEBIAN_FRONTEND=noninteractive apt-get -y upgrade --no-install-recommends
RUN echo "root:root" | chpasswd
RUN echo "PermitRootLogin yes" >>/etc/ssh/sshd_config
RUN echo "auto ens3\niface ens3 inet dhcp" >/etc/network/interfaces
RUN echo "supersede domain-name-servers 8.8.8.8;" >>/etc/dhcp/dhclient.conf
`

	hosts = `
127.0.0.1 localhost
::1 localhost
`

	fstab = `
/dev/vda1 / ext4 rw,relatime 0 1
bpffs /sys/fs/bpf bpf rw,relatime 0 0
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
bpffs /sys/fs/bpf bpf rw,relatime 0 1
EOF

update-initramfs -u

grub-install /dev/vda
perl -pi -e 's/GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
update-grub2

rm /etc/machine-id /var/lib/dbus/machine-id
touch /etc/machine-id
chattr +i /etc/machine-id
sync
`
)

type BuildCustomizeFunc func(*VM) error

type BuildConfig struct {
	OutputPath     string
	TempDir        string
	CustomizeFuncs []BuildCustomizeFunc
	BuildLog       io.Writer
	NoKVM          bool
}

func BuildImage(ctx context.Context, cfg *BuildConfig) error {
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

	u, err := New(ctx, tmp)
	if err != nil {
		return fmt.Errorf("creating virtuakube instance: %v", err)
	}
	defer u.Close()
	v, err := u.NewVM(&VMConfig{
		Image:      imgPath,
		MemoryMiB:  2048,
		CommandLog: cfg.BuildLog,
		NoKVM:      cfg.NoKVM,
		kernelPath: filepath.Join(tmp, "vmlinuz"),
		initrdPath: filepath.Join(tmp, "initrd.img"),
		cmdline:    "root=/dev/vda1 rw",
	})
	if err != nil {
		return fmt.Errorf("creating image VM: %v", err)
	}
	if err := v.Start(); err != nil {
		return fmt.Errorf("starting image VM: %v", err)
	}

	if err := v.WriteFile("/etc/hosts", []byte(hosts)); err != nil {
		return fmt.Errorf("install /etc/hosts: %v", err)
	}

	if err := v.WriteFile("/etc/fstab", []byte(fstab)); err != nil {
		return fmt.Errorf("install /etc/fstab: %v", err)
	}

	err = v.RunMultiple(
		"update-initramfs -u",

		"grub-install /dev/vda",
		"perl -pi -e 's/GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub",
		"update-grub2",

		"rm /etc/machine-id /var/lib/dbus/machine-id",
		"touch /etc/machine-id",
		"chattr +i /etc/machine-id",
	)
	if err != nil {
		return fmt.Errorf("finalize base image configuration: %v", err)
	}

	for _, f := range cfg.CustomizeFuncs {
		if err = f(v); err != nil {
			return fmt.Errorf("applying customize func: %v", err)
		}
	}

	if _, err := v.Run("sync"); err != nil {
		return fmt.Errorf("syncing image disk: %v", err)
	}

	// Shut down the VM. Shutdown triggers destruction of the
	// universe, so we can wait for the context to get canceled.
	v.Run("poweroff")
	if err := v.Wait(context.Background()); err != nil {
		return fmt.Errorf("waiting for VM shutdown: %v", err)
	}

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

func CustomizeInstallK8s(v *VM) error {
	repos := []byte(`
deb [arch=amd64] https://download.docker.com/linux/debian stretch stable
deb http://apt.kubernetes.io/ kubernetes-xenial main
`)
	if err := v.WriteFile("/etc/apt/sources.list.d/k8s.list", repos); err != nil {
		return err
	}

	pkgs := []string{
		"apt-transport-https",
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
	err := v.RunMultiple(
		"DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends "+strings.Join(pkgs, " "),
		"curl -fsSL https://download.docker.com/linux/debian/gpg | apt-key add -",
		"curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -",
		"apt-get -y update",
		"DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends "+strings.Join(k8sPkgs, " "),
		"echo br_netfilter >>/etc/modules",
		"echo export KUBECONFIG=/etc/kubernetes/admin.conf >>/etc/profile.d/k8s.sh",
	)
	if err != nil {
		return err
	}

	return nil
}

func CustomizePreloadK8sImages(v *VM) error {
	err := v.RunMultiple(
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

	return nil
}
