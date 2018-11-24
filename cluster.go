package virtuakube

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"go.universe.tf/virtuakube/internal/bootscript"
)

var incrClusterID = make(chan int)

func init() {
	id := 1
	go func() {
		for {
			incrClusterID <- id
			id++
		}
	}()
}

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
	cfg        *ClusterConfig
	tmpdir     string
	kubeconfig string
	client     *kubernetes.Clientset

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

	p, err := u.Tmpdir("cluster")
	if err != nil {
		return nil, err
	}

	ret := &Cluster{
		cfg:    cfg,
		tmpdir: p,
	}

	clusterID := <-incrClusterID

	masterCfg := cfg.VMConfig
	masterCfg.Hostname = fmt.Sprintf("cluster%d-master", clusterID)
	masterCfg.BootScript = bootscript.MustAsset("master.sh")
	masterCfg.PortForwards = []int{22, 5000, 6443}
	ret.master, err = u.NewVM(&masterCfg)
	if err != nil {
		return nil, err
	}

	for i := 0; i < cfg.NumNodes; i++ {
		nodeCfg := cfg.VMConfig
		nodeCfg.Hostname = fmt.Sprintf("cluster%d-node%d", clusterID, i+1)
		nodeCfg.BootScript = bootscript.MustAsset("node.sh")
		nodeCfg.PortForwards = []int{22, 5000}
		node, err := u.NewVM(&nodeCfg)
		if err != nil {
			return nil, err
		}
		ret.nodes = append(ret.nodes, node)
	}

	return ret, nil
}

func (c *Cluster) Start() error {
	if err := copyFile(c.cfg.NetworkAddon, filepath.Join(c.master.Dir(), "addons.yaml")); err != nil {
		return err
	}

	if err := c.master.Start(); err != nil {
		return err
	}
	return nil
	for _, node := range c.nodes {
		if err := node.Start(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cluster) WaitReady(ctx context.Context) error {
	if err := c.master.WaitReady(ctx); err != nil {
		return err
	}
	return nil
	for _, node := range c.nodes {
		if err := node.WaitReady(ctx); err != nil {
			return err
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", c.Kubeconfig())
	if err != nil {
		return err
	}

	c.client, err = kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	err = waitFor(ctx, func() (bool, error) {
		nodes, err := c.client.CoreV1().Nodes().List(metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		if len(nodes.Items) != c.cfg.NumNodes+1 {
			return false, nil
		}
		for _, node := range nodes.Items {
			if !nodeReady(node) {
				return false, nil
			}
		}

		deploys, err := c.client.AppsV1().Deployments("").List(metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, deploy := range deploys.Items {
			if deploy.Status.AvailableReplicas != deploy.Status.Replicas {
				return false, nil
			}
		}

		daemons, err := c.client.AppsV1().DaemonSets("").List(metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, daemon := range daemons.Items {
			if daemon.Status.NumberAvailable != daemon.Status.DesiredNumberScheduled {
				return false, nil
			}
		}

		return true, nil
	})

	return nil
}

func (c *Cluster) Kubeconfig() string {
	return filepath.Join(c.master.Dir(), "kubeconfig")
}

func nodeReady(node corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type != corev1.NodeReady {
			continue
		}
		if cond.Status == corev1.ConditionTrue {
			return true
		}
		return false
	}

	return false
}

func copyFile(src, dst string) error {
	bs, err := ioutil.ReadFile(src)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(dst, bs, 0644); err != nil {
		return err
	}
	return nil
}

func waitFor(ctx context.Context, test func() (bool, error)) error {
	done := ctx.Done()
	for {
		select {
		case <-done:
			return errors.New("timeout")
		default:
		}

		ok, err := test()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}
}
