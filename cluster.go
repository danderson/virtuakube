package virtuakube

import (
	"errors"
	"os"
	"path/filepath"
)

type ClusterConfig struct {
	// NumNodes is the number of Kubernetes worker nodes to run.
	// TODO: only supports 1 currently
	NumNodes int
	// The VMConfig to use when creating cluster VMs.
	VMConfig
	// NetworkAddon is the Kubernetes network addon to install. Can be
	// an absolute path to a manifest yaml, or one of the builtin
	// addons "calico" or "weave".
	NetworkAddon string
	// ExtraAddons is a list of Kubernetes manifest yamls to apply to
	// the cluster, in addition to the network addon.
	ExtraAddons []string
}

type Cluster struct {
	cfg *ClusterConfig

	master *VM
	nodes  []*VM
}

func validateClusterConfig(cfg *ClusterConfig) error {
	if cfg.NumNodes != 1 {
		return errors.New("clusters with >1 node not supported yet")
	}

	if err := validateVMConfig(&cfg.VMConfig); err != nil {
		return err
	}

	if cfg.NetworkAddon == "" {
		return errors.New("must specify network addon")
	}
	nap, err := filepath.Abs(cfg.NetworkAddon)
	if err != nil {
		return err
	}
	if _, err := os.Stat(nap); err != nil {
		return err
	}
	cfg.NetworkAddon = nap

	for i, extra := range cfg.ExtraAddons {
		eap, err := filepath.Abs(extra)
		if err != nil {
			return err
		}
		if _, err := os.Stat(eap); err != nil {
			return err
		}
		cfg.ExtraAddons[i] = eap
	}

	return nil
}

func (u *Universe) NewCluster(cfg *ClusterConfig) (*Cluster, error) {
	if err := validateClusterConfig(cfg); err != nil {
		return nil, err
	}

	return nil, nil
}
