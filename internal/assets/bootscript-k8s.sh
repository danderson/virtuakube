#!/bin/bash

set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive
# Updated by `make update-addons`
ADDONS="registry:2 quay.io/calico/typha:v3.3.1 quay.io/calico/node:v3.3.1 quay.io/calico/cni:v3.3.1 docker.io/weaveworks/weave-kube:2.5.0 docker.io/weaveworks/weave-npc:2.5.0 "

# Get DNS and networking up.
cat >/etc/systemd.resolved.conf <<EOF
[Resolve]
DNSSEC=no
DNSStubListener=yes
LLMNR=yes
MulticastDNS=yes
EOF
echo "nameserver 127.0.0.53" >/etc/resolv.conf
systemctl start systemd-resolved

cat >/etc/systemd/network/20-lan.network <<EOF
[Match]

[Network]
LinkLocalAddressing=ipv6
LLMNR=yes
MulticastDNS=yes
EOF
systemctl start systemd-networkd

# Install Docker, Kubernetes, and other utility things.
mkdir -p /etc/docker
cat >/etc/docker/daemon.json <<EOF
{
  "insecure-registries" : ["localhost:30000"]
}
EOF
apt-get -y update
apt-get -y install --no-install-recommends openssh-server ebtables ethtool curl gpg gpg-agent software-properties-common systemd-sysv isc-dhcp-client linux-image-amd64 dbus grub2 bird policykit-1
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

# Enable essential services for future boots.
systemctl enable systemd-resolved systemd-networkd

# Pre-pull images for Kubernetes and the addon pods we care about.
systemctl start docker
kubeadm config images pull

echo $ADDONS | xargs -n1 docker pull

# Rebuild the initramfs, to get rid of an irritating fsck error
# because of a partial build in the container universe.
update-initramfs -u

# Install grub.
grub-install --no-floppy /dev/vda
perl -pi -e 's/GRUB_TIMEOUT=.*/GRUB_TIMEOUT=0/' /etc/default/grub
update-grub2
