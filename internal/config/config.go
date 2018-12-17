package config

import (
	"encoding/json"
	"io/ioutil"
	"net"
	"time"
)

type Universe struct {
	Snapshots map[string]*Snapshot
}

type Snapshot struct {
	Name     string
	ID       string
	NextPort int
	NextIPv4 net.IP
	NextIPv6 net.IP
	Clock    time.Time

	Images   map[string]*Image
	VMs      map[string]*VM
	Clusters map[string]*Cluster
}

type Image struct {
	Name string
	File string
}

type VM struct {
	Name         string
	DiskFile     string
	MemoryMiB    int
	PortForwards map[int]int
	MAC          string
	IPv4         net.IP
	IPv6         net.IP
}

type Cluster struct {
	Name       string
	NumNodes   int
	Kubeconfig []byte
}

func Read(path string) (*Universe, error) {
	bs, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ret Universe
	if err := json.Unmarshal(bs, &ret); err != nil {
		return nil, err
	}

	return &ret, nil
}

func Write(path string, cfg *Universe) error {
	bs, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(path, bs, 0600); err != nil {
		return err
	}
	return nil
}
