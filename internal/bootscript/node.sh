#!/bin/bash

set -euxo pipefail

trap "touch /host/bootscript-done" EXIT

MASTER_HOSTNAME="$(hostname | cut -f1 -d'-')-master"

IP=192.168.50.2
ip addr add ${IP}/24 dev ens4
kubeadm join --token=000000.0000000000000000 --discovery-token-unsafe-skip-ca-verification ${MASTER_HOSTNAME}.local:6443
