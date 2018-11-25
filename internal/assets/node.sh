#!/bin/bash

set -euxo pipefail

CONTROLLER_HOSTNAME="$(hostname | cut -f1 -d'-')-controller"

kubeadm join --token=000000.0000000000000000 --discovery-token-unsafe-skip-ca-verification ${CONTROLLER_HOSTNAME}.local:6443
