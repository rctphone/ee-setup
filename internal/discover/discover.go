// Package discover locates the router and identifies its model.
//
// Addresses are not stable: the LAN subnet differs per firmware and users move
// it. The probe therefore tries, in order: an explicit address, the two known
// DNS names, the factory default, and the .1 host of every local subnet.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rctphone/ee-setup/internal/ee71api"
	"github.com/rctphone/ee-setup/internal/tclapi"
)

// Model identifies the router family.
type Model string

const (
	ModelEE71 Model = "EE71"
	ModelEE72 Model = "EE72"
)

// Device is a router that answered the probe.
type Device struct {
	Model Model
	Host  string // address used to reach it
	VHost string // Host header required by the EE72 web server
	Info  map[string]any
}

// ErrNotFound means no candidate answered as a supported router.
var ErrNotFound = errors.New("no EE71/EE72 web interface found")

// nameCandidates resolve through the router's own DHCP/DNS.
var nameCandidates = []string{"4g.wifi", "4gee.wifi"}

// addrCandidates is the single factory default worth trying. Per-firmware LAN
// addresses are not hard-coded: they differ and are covered by the local-subnet
// scan below.
var addrCandidates = []string{"192.168.1.1"}

// Candidates returns the addresses worth probing, explicit host first.
func Candidates(explicit string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(explicit)
	for _, n := range nameCandidates {
		add(n)
	}
	for _, a := range addrCandidates {
		add(a)
	}
	for _, a := range localSubnetHosts() {
		add(a)
	}
	return out
}

// localSubnetHosts returns <network>.1 for every private IPv4 interface.
func localSubnetHosts() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || !ipnet.IP.IsPrivate() {
				continue
			}
			gw := make(net.IP, 4)
			copy(gw, ipnet.IP.To4())
			gw[3] = 1
			out = append(out, gw.String())
		}
	}
	sort.Strings(out)
	return out
}

// Probe tries one host with both protocol variants.
func Probe(ctx context.Context, host, password string) (*Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	// EE72 first: its result envelope must decrypt with the public key.
	c72 := tclapi.New(host, password)
	if salt, err := c72.Salt(ctx); err == nil && salt != "" {
		d := &Device{Model: ModelEE72, Host: host, VHost: tclapi.DefaultVHost}
		if raw, err := c72.CallPublic(ctx, "GetSystemInfo", struct{}{}); err == nil {
			_ = json.Unmarshal(raw, &d.Info)
		}
		return d, nil
	}

	// Neither protocol answered: fingerprint the web server itself.
	if m := homeModel(ctx, host); m != "" {
		return &Device{Model: m, Host: host, VHost: tclapi.DefaultVHost}, nil
	}

	// EE71 answers the same method with plain JSON.
	c71 := ee71api.New(host, password)
	if salt, err := c71.Salt(ctx); err == nil && salt != "" {
		d := &Device{Model: ModelEE71, Host: host}
		if raw, err := c71.Call(ctx, "GetSystemInfo", struct{}{}); err == nil {
			_ = json.Unmarshal(raw, &d.Info)
		}
		return d, nil
	}
	return nil, ErrNotFound
}

// Find probes all candidates concurrently and returns the first that answers.
func Find(ctx context.Context, explicit, password string) (*Device, error) {
	cands := Candidates(explicit)
	type result struct {
		d   *Device
		err error
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan result, len(cands))
	for _, host := range cands {
		go func(host string) {
			d, err := Probe(ctx, host, password)
			ch <- result{d, err}
		}(host)
	}

	var firstErr error
	for range cands {
		r := <-ch
		if r.err == nil {
			cancel()
			return r.d, nil
		}
		if firstErr == nil {
			firstErr = r.err
		}
	}
	if firstErr == nil {
		firstErr = ErrNotFound
	}
	return nil, firstErr
}

// homeModel fingerprints the web server. EE72 redirects any request whose Host
// is not 4g.wifi; the page body may also name the model.
func homeModel(ctx context.Context, host string) Model {
	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/", nil)
	if err != nil {
		return ""
	}
	req.Host = "ee-setup.invalid"
	if resp, err := client.Do(req); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 &&
			strings.Contains(resp.Header.Get("Location"), tclapi.DefaultVHost) {
			return ModelEE72
		}
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/", nil)
	if err != nil {
		return ""
	}
	req.Host = tclapi.DefaultVHost
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case strings.Contains(string(body), "EE72"):
		return ModelEE72
	case strings.Contains(string(body), "EE71"):
		return ModelEE71
	}
	return ""
}

// Version returns a human-readable firmware string from Info, if present.
func (d *Device) Version() string {
	for _, k := range []string{"SoftwareVersion", "software_version", "Version", "version",
		"FirmwareVersion", "firmware_version", "BuildVersion", "build_version"} {
		if v, ok := d.Info[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
