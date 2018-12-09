package virtuakube

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.universe.tf/virtuakube/internal/assets"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// ClusterConfig is the configuration for a virtual Kubernetes
// cluster.
type ClusterConfig struct {
	Name string
	// NumNodes is the number of Kubernetes worker nodes to run.
	// TODO: only supports 1 currently
	NumNodes int
	// The VMConfig template to use when creating cluster VMs.
	VMConfig *VMConfig
	// NetworkAddon is the Kubernetes network addon to install. Can be
	// an absolute path to a manifest yaml, or one of the builtin
	// addons "calico" or "weave".
	NetworkAddon string
}

// Copy returns a deep copy of the cluster config.
func (c *ClusterConfig) Copy() *ClusterConfig {
	ret := &ClusterConfig{
		Name:         c.Name,
		NumNodes:     c.NumNodes,
		VMConfig:     c.VMConfig.Copy(),
		NetworkAddon: c.NetworkAddon,
	}
	return ret
}

// Cluster is a virtual Kubernetes cluster.
type Cluster struct {
	cfg *ClusterConfig

	dir string

	mu sync.Mutex

	// Kubernetes client connected to the cluster.
	client *kubernetes.Clientset

	// Cluster VMs.
	controller *VM
	nodes      []*VM

	started bool
}

func randomClusterName() string {
	rnd := make([]byte, 6)
	if _, err := rand.Read(rnd); err != nil {
		panic("system ran out of randomness")
	}
	return fmt.Sprintf("cluster%x", rnd)
}

func validateClusterConfig(cfg *ClusterConfig) (*ClusterConfig, error) {
	cfg = cfg.Copy()

	if cfg.VMConfig == nil {
		return nil, errors.New("missing VMConfig")
	}

	var err error
	cfg.VMConfig, err = validateVMConfig(cfg.VMConfig)
	if err != nil {
		return nil, err
	}

	if cfg.Name == "" {
		cfg.Name = randomClusterName()
	}

	if cfg.NetworkAddon == "" {
		return nil, errors.New("must specify network addon")
	}

	return cfg, nil
}

// NewCluster creates an unstarted Kubernetes cluster with the given
// configuration.
func (u *Universe) NewCluster(cfg *ClusterConfig) (*Cluster, error) {
	cfg, err := validateClusterConfig(cfg)
	if err != nil {
		return nil, err
	}

	if u.Cluster(cfg.Name) != nil {
		return nil, fmt.Errorf("universe already has a cluster named %q", cfg.Name)
	}

	dir := filepath.Join(u.dir, "cluster", cfg.Name)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("creating cluster state dir: %v", err)
	}

	ret := &Cluster{
		cfg: cfg,
		dir: dir,
	}

	controllerCfg := cfg.VMConfig.Copy()
	controllerCfg.Hostname = fmt.Sprintf("%s-controller", cfg.Name)
	controllerCfg.PortForwards[30000] = true
	controllerCfg.PortForwards[6443] = true
	ret.controller, err = u.NewVM(controllerCfg)
	if err != nil {
		return nil, fmt.Errorf("creating controller VM: %v", err)
	}

	for i := 0; i < cfg.NumNodes; i++ {
		nodeCfg := cfg.VMConfig.Copy()
		nodeCfg.Hostname = fmt.Sprintf("%s-node%d", cfg.Name, i+1)
		node, err := u.NewVM(nodeCfg)
		if err != nil {
			return nil, fmt.Errorf("creating node %d: %v", i+1, err)
		}
		ret.nodes = append(ret.nodes, node)
	}

	u.mu.Lock()
	u.mu.Unlock()
	if u.clusters[cfg.Name] != nil {
		return nil, fmt.Errorf("universe already has a VM named %q", cfg.Name)
	}
	u.clusters[cfg.Name] = ret
	u.newClusters[cfg.Name] = true
	return ret, nil
}

func (u *Universe) thawCluster(name string) (*Cluster, error) {
	if u.Cluster(name) != nil {
		return nil, fmt.Errorf("universe already has a cluster named %q", name)
	}

	dir := filepath.Join(u.dir, "cluster", name)

	bs, err := ioutil.ReadFile(filepath.Join(dir, "cluster.json"))
	if err != nil {
		return nil, err
	}
	var cfg ClusterConfig
	if err := json.Unmarshal(bs, &cfg); err != nil {
		return nil, err
	}

	ret := &Cluster{
		cfg:     &cfg,
		dir:     dir,
		started: true,
	}

	ret.controller = u.VM(fmt.Sprintf("%s-controller", cfg.Name))
	for i := 0; i < ret.cfg.NumNodes; i++ {
		ret.nodes = append(ret.nodes, u.VM(fmt.Sprintf("%s-node%d", cfg.Name, i+1)))
	}

	if err := ret.mkKubeClient(); err != nil {
		return nil, err
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.clusters[cfg.Name] != nil {
		return nil, fmt.Errorf("universe already has a VM named %q", cfg.Name)
	}
	u.clusters[name] = ret

	return ret, nil
}

// Start boots the virtual cluster. The universe is destroyed if any
// VM in the cluster shuts down.
func (c *Cluster) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return errors.New("already started")
	}
	c.started = true

	if err := c.startController(); err != nil {
		return err
	}

	for _, node := range c.nodes {
		// TODO: scatter-gather startup
		if err := c.startNode(node); err != nil {
			return err
		}
	}

	// TODO: this would be a useful public helper of some kind.
	err := waitFor(func() (bool, error) {
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
	if err != nil {
		return err
	}

	return nil
}

func (c *Cluster) mkKubeClient() error {
	config, err := clientcmd.BuildConfigFromFlags("", c.Kubeconfig())
	if err != nil {
		return err
	}

	c.client, err = kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	return nil
}

var addrRe = regexp.MustCompile("https://.*:6443")

func (c *Cluster) startController() error {
	addons, err := assembleAddons(c.cfg.NetworkAddon)
	if err != nil {
		return err
	}

	if err := c.controller.Start(); err != nil {
		return err
	}

	controllerConfig := fmt.Sprintf(`
apiVersion: kubeadm.k8s.io/v1alpha3
kind: InitConfiguration
bootstrapTokens:
- token: "000000.0000000000000000"
  ttl: "24h"
apiEndpoint:
  advertiseAddress: %s
nodeRegistration:
  kubeletExtraArgs:
    node-ip: %s
---
apiVersion: kubeadm.k8s.io/v1alpha3
kind: ClusterConfiguration
networking:
  podSubnet: "10.32.0.0/12"
kubernetesVersion: "1.13.0"
clusterName: "virtuakube"
apiServerCertSANs:
- "127.0.0.1"
`, c.controller.IPv4(), c.controller.IPv4())
	if err := c.controller.WriteFile("/tmp/k8s.conf", []byte(controllerConfig)); err != nil {
		return err
	}
	if err := c.controller.WriteFile("/tmp/addons.yaml", addons); err != nil {
		return err
	}

	err = c.controller.RunMultiple(
		"kubeadm init --config=/tmp/k8s.conf --ignore-preflight-errors=NumCPU",
		"KUBECONFIG=/etc/kubernetes/admin.conf kubectl taint nodes --all node-role.kubernetes.io/master-",
		"KUBECONFIG=/etc/kubernetes/admin.conf kubectl apply -f /tmp/addons.yaml",
	)
	if err != nil {
		return err
	}

	kubeconfig, err := c.controller.ReadFile("/etc/kubernetes/admin.conf")
	if err != nil {
		return err
	}
	kubeconfig = addrRe.ReplaceAll(kubeconfig, []byte("https://127.0.0.1:"+strconv.Itoa(c.controller.ForwardedPort(6443))))
	if err := ioutil.WriteFile(c.Kubeconfig(), kubeconfig, 0600); err != nil {
		return err
	}

	return c.mkKubeClient()
}

func (c *Cluster) startNode(node *VM) error {
	if err := node.Start(); err != nil {
		return err
	}

	controllerAddr := &net.TCPAddr{
		IP:   c.controller.IPv4(),
		Port: 6443,
	}
	nodeConfig := fmt.Sprintf(`
apiVersion: kubeadm.k8s.io/v1alpha3
kind: JoinConfiguration
token: "000000.0000000000000000"
discoveryTokenUnsafeSkipCAVerification: true
discoveryTokenAPIServers:
- %s
nodeRegistration:
  kubeletExtraArgs:
    node-ip: %s
`, controllerAddr, node.IPv4())
	if err := node.WriteFile("/tmp/k8s.conf", []byte(nodeConfig)); err != nil {
		return err
	}

	if _, err := node.Run("kubeadm join --config=/tmp/k8s.conf"); err != nil {
		return err
	}

	return nil
}

func (c *Cluster) Name() string {
	return c.cfg.Name
}

// Kubeconfig returns the path to a kubectl configuration file with
// administrator credentials for the cluster.
func (c *Cluster) Kubeconfig() string {
	return filepath.Join(c.dir, "kubeconfig")
}

// KubernetesClient returns a kubernetes client connected to the
// cluster.
func (c *Cluster) KubernetesClient() *kubernetes.Clientset {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

// Controller returns the VM for the cluster controller node.
func (c *Cluster) Controller() *VM {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.controller
}

// Nodes returns the VMs for the cluster nodes.
func (c *Cluster) Nodes() []*VM {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nodes
}

// Registry returns the port on localhost for the in-cluster
// registry. Within the cluster, the registry is reachable at
// localhost:30000 on all nodes.
func (c *Cluster) Registry() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.controller.ForwardedPort(30000)
}

func (c *Cluster) freeze() error {
	bs, err := ioutil.ReadFile(c.Kubeconfig())
	if err != nil {
		return fmt.Errorf("reading kubeconfig: %v", err)
	}

	c.cfg.VMConfig.CommandLog = nil

	bs, err = json.MarshalIndent(c.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling cluster config: %v", err)
	}

	if err := ioutil.WriteFile(filepath.Join(c.dir, "cluster.json"), bs, 0600); err != nil {
		return fmt.Errorf("writing cluster config: %v", err)
	}

	return nil
}

func networkAddonBytes(addon string) ([]byte, error) {
	if !strings.ContainsAny(addon, "./") {
		bs, err := assets.Asset("net/" + addon + ".yaml")
		if err == nil {
			return bs, nil
		}
	}

	return ioutil.ReadFile(addon)
}

func assembleAddons(networkAddon string) ([]byte, error) {
	var out [][]byte

	bs, err := networkAddonBytes(networkAddon)
	if err != nil {
		return nil, err
	}
	out = append(out, bs)
	out = append(out, assets.MustAsset("registry.yaml"))

	return bytes.Join(out, []byte("\n---\n")), nil
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

func waitFor(test func() (bool, error)) error {
	for {
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
