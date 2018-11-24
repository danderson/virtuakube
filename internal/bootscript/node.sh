#!/bin/bash

set -euxo pipefail

MASTER_HOSTNAME="$(hostname | cut -f1 -d'-')-master"

kubeadm join --token=000000.0000000000000000 --discovery-token-unsafe-skip-ca-verification ${MASTER_HOSTNAME}.local:6443
