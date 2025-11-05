package dhcpd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	configName = "leases.json"
)

type Lease struct {
	IP         string `json:"ip,omitempty"`
	MACAddress string `json:"macAddress,omitempty"`
	Expires    int64  `json:"expires,omitempty"`
}

type Query struct {
	IP         string
	MACAddress string
}

type LeaseDB struct {
	Hosts map[string]*Lease `json:"hosts,omitempty"`
	IPs   map[string]*Lease `json:"ips,omitempty"`
}

func newLeaseDB() *LeaseDB {
	return &LeaseDB{
		Hosts: map[string]*Lease{},
		IPs:   map[string]*Lease{},
	}
}

type Datastore struct {
	db         *LeaseDB
	configPath string

	mu *sync.Mutex
}

// NewDatastore returns a new simple Lease datastore
func NewDatastore(path string) (*Datastore, error) {
	configPath := filepath.Join(path, configName)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}

	ds := &Datastore{
		db:         newLeaseDB(),
		configPath: configPath,
		mu:         &sync.Mutex{},
	}

	// load existing
	if _, err := os.Stat(configPath); err == nil {
		f, err := os.Open(configPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		var db LeaseDB
		if err := json.NewDecoder(f).Decode(&db); err != nil {
			return nil, err
		}
		// check for nil
		if db.Hosts == nil {
			db.Hosts = map[string]*Lease{}
		}
		if db.IPs == nil {
			db.IPs = map[string]*Lease{}
		}
		ds.db = &db
	}

	return ds, nil
}

func (d *Datastore) Reserve(macAddress, ip string, ttl time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if v, ok := d.db.Hosts[macAddress]; ok {
		// already reserved
		if v.IP == ip {
			return nil
		}
		// collision
		return fmt.Errorf("%w: IP %q is already reserved for %s", ErrExists, v.IP, v.MACAddress)
	}

	lease := &Lease{
		IP:         ip,
		MACAddress: macAddress,
		Expires:    time.Now().Add(ttl).UnixNano(),
	}

	// index both host and ip for faster lookup
	d.db.Hosts[macAddress] = lease
	d.db.IPs[ip] = lease

	// save
	if err := d.saveDB(); err != nil {
		return err
	}

	return nil
}

func (d *Datastore) Get(q *Query) (*Lease, error) {
	if v := q.IP; v != "" {
		return d.getLeaseByIP(v)
	}

	if v := q.MACAddress; v != "" {
		return d.getLeaseByMAC(v)
	}

	return nil, fmt.Errorf("%w: %v", ErrNotFound, q)
}

func (d *Datastore) getLeaseByIP(ip string) (*Lease, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.db.IPs[ip]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, ip)
	}

	return v, nil
}

func (d *Datastore) getLeaseByMAC(addr string) (*Lease, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	v, ok := d.db.Hosts[addr]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, addr)
	}

	return v, nil
}

func (d *Datastore) List() ([]*Lease, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	leases := make([]*Lease, 0, len(d.db.Hosts))
	for _, v := range d.db.Hosts {
		leases = append(leases, v)
	}

	return leases, nil
}

func (d *Datastore) Release(ip string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// delete from ips
	lease, ok := d.db.IPs[ip]
	if ok {
		delete(d.db.IPs, ip)
	}

	// delete from hosts
	if lease != nil {
		delete(d.db.Hosts, lease.MACAddress)
	}

	if err := d.saveDB(); err != nil {
		return err
	}

	return nil
}

func (d *Datastore) saveDB() error {
	data, err := json.Marshal(d.db)
	if err != nil {
		return err
	}

	if err := os.WriteFile(d.configPath, data, 0o660); err != nil {
		return err
	}

	return nil
}
