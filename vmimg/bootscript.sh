#!/bin/bash

set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive

# Get DNS and networking up.
cp -f /host/resolved.conf /etc/systemd/resolved.conf
echo "nameserver 127.0.0.53" >/etc/resolv.conf
systemctl start systemd-resolved

cp /host/networkd/* /etc/systemd/network
systemctl start systemd-networkd

# Install Docker, Kubernetes, and other utility things.
mkdir -p /etc/docker
cat >/etc/docker/daemon.json <<EOF
{
  "insecure-registries" : ["localhost:5000"]
}
EOF
apt-get -y update
apt-get -y install --no-install-recommends openssh-server ebtables ethtool curl gpg gpg-agent software-properties-common systemd-sysv isc-dhcp-client linux-image-amd64 dbus grub2 bird policykit-1
echo "PermitRootLogin yes" >>/etc/ssh/sshd_config
echo "br_netfilter" >>/etc/modules
curl -fsSL https://download.docker.com/linux/debian/gpg | apt-key add -
apt-add-repository "deb [arch=amd64] https://download.docker.com/linux/debian buster stable"
curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -
add-apt-repository "deb http://apt.kubernetes.io/ kubernetes-xenial main"
apt-get -y update
apt-get -y install --no-install-recommends docker-ce=18.06.* kubelet kubeadm kubectl
apt-get -y purge --autoremove gpg gpg-agent software-properties-common
# Shut down kubelet for the remainder of this particular script,
# because it spams the system journal with its crashloops.
systemctl stop kubelet

cp /host/systemd/* /etc/systemd/system
systemctl daemon-reload

# Enable essential services for future boots.
systemctl enable systemd-resolved systemd-networkd registry

# Pre-pull images for Kubernetes and the addon pods we care about.
systemctl start docker
kubeadm config images pull
cat /host/addon-images | xargs -n1 docker pull

# Rebuild the initramfs, to get rid of an irritating fsck error
# because of a partial build in the container universe.
update-initramfs -u

# Install grub.
grub-install --no-floppy /dev/vda
perl -pi -e 's/GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
update-grub2

poweroff
