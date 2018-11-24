#!/bin/bash
#
# This script contains just enough to result in a filesystem that can
# boot linux and execute a host-provided bootscript to do arbitrary
# other things.

set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive

apt-get -y update
apt-get -y install --no-install-recommends systemd-sysv linux-image-amd64 dbus
echo "root:root" | chpasswd
mkdir /host
cat >/etc/fstab <<EOF
/dev/vda1 / ext4 rw,relatime 0 1
host0 /host 9p trans=virtio,version=9p2000.L 0 0
EOF

cat >/etc/initramfs-tools/modules <<EOF
9p
9pnet
9pnet_virtio
EOF
update-initramfs -u
cat >/boot.sh <<EOF
#!/bin/bash

set -euxo pipefail

trap "touch /host/boot-done" EXIT

if [[ -f /host/ip ]]; then
  for ip in \$(cat /host/ip); do
    ip addr add \$ip/24 dev ens4
  done
fi

if [[ -f /host/bootscript.sh ]]; then
  /host/bootscript.sh
fi
EOF
chmod 555 /boot.sh
cat >/etc/systemd/system/bootscript.service <<EOF
[Unit]
After=network-online.target
RequiresMountsFor=/host

[Service]
Type=oneshot
ExecStart=/boot.sh

[Install]
WantedBy=multi-user.target
EOF
systemctl enable bootscript.service
