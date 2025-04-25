// Minidisc service discovery.

package minidisc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sync"
	"time"
)

// Service represents a network service on the Tailnet.
type Service struct {
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels"`
	AddrPort netip.AddrPort    `json:"addrPort"`
}

// Read API ////////////////////////////////////////////////////////////////////

// ListServices queries and combines the advertised services from all Minidisc
// registries on the Tailnet.
func ListServices() ([]Service, error) {
	var results []Service
	var channels []chan []Service
	// List IPv4 addresses of online nodes on the Tailnet.
	addrs, err := listTailnetAddrs()
	if err != nil {
		return results, err
	}
	// Kick off queries to each of them in parallel.
	for _, addr := range addrs {
		ap := netip.AddrPortFrom(addr, 28004)
		ch := make(chan []Service)
		channels = append(channels, ch)
		go func() {
			defer close(ch)
			if services, err := getRemoteServices(ap); err == nil {
				ch <- services
			} else if !isUrlError(err) {
				logger.Warnf("Error fetching services from %s: %v", ap.String(), err)
			} else {
				logger.Debugf("Error connecting to %s: %v", ap.String(), err)
			}
		}()
	}
	// Wait for and concatenate the results.
	for _, ch := range channels {
		if part, ok := <-ch; ok {
			results = slices.Concat(results, part)
		}
	}
	return results, nil
}

// FindService tries to find a service that matches the name and the given
// labels. If several services match, it returns the first one to be found.
// Only requested labels get compared - if the request asks for env=prod, this
// will match [env=prod], [env=prod, foo=bar], but not [env=staging].
func FindService(name string, labels map[string]string) (netip.AddrPort, error) {
	ss, err := ListServices()
	if err != nil {
		return netip.AddrPort{}, err
	}
	for _, s := range ss {
		if serviceMatches(s, name, labels) {
			return s.AddrPort, nil
		}
	}
	return netip.AddrPort{}, fmt.Errorf("No matching service found")
}

// getRemoteServices fetches advertised services from a remote registry.
func getRemoteServices(ap netip.AddrPort) ([]Service, error) {
	var result []Service
	c := http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s/services", ap.String())
	resp, err := c.Get(url)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(body, &result)
	return result, err
}

func isUrlError(err error) bool {
	_, ok := err.(*url.Error)
	return ok
}

// serviceMatches implements the matching logic for FindService.
func serviceMatches(s Service, name string, labels map[string]string) bool {
	if s.Name != name {
		return false
	}
	for k, v := range labels {
		sv, ok := s.Labels[k]
		if !ok || v != sv {
			return false
		}
	}
	return true
}

// Local Registry API //////////////////////////////////////////////////////////

// Registry is the local interface to the Minidisc service discovery. It
// maintains and advertises a list of services that the current process offers.
type Registry struct {
	http.Handler

	mutex sync.Mutex
	// The local Tailnet IPv4 address of the local host. We set this at init
	// time to be robust against host's admin switching to a different Tailnet.
	localAddr     netip.Addr
	localServices []Service
	delegates     []netip.AddrPort
}

// StartRegistry creates a local Minidisc registry and starts the goroutines
// that keep it up-to-date and connected to other registries on the Tailnet.
func StartRegistry() (*Registry, error) {
	tmap, err := getTailnetMap()
	if err != nil {
		return nil, err
	}
	r := &Registry{
		localAddr:     tmap.LocalAddr,
		localServices: []Service{}, // Empty list, but JSON marshal-able.
	}
	logger.Infof("Starting Minidisc registry")
	go r.connect()
	return r, nil
}

// AdvertiseService adds a local service to the list this registry advertises.
func (r *Registry) AdvertiseService(port uint16, name string, labels map[string]string) error {
	ap := netip.AddrPortFrom(r.localAddr, port)
	return r.addService(ap, name, labels)
}

// AdvertiseRemoteService adds a remote service to the list this registry
// advertises. You should only do this to include services that aren't minidisc
// enabled themselves.
func (r *Registry) AdvertiseRemoteService(
	addrPort netip.AddrPort, name string, labels map[string]string,
) error {
	if prefix, err := addrPort.Addr().Prefix(8); err != nil {
		panic(err) // Only happens on bad params
	} else if prefix != netip.MustParsePrefix("100.0.0.0/8") {
		return fmt.Errorf("Non-tailscale address %s", addrPort.String())
	}
	return r.addService(addrPort, name, labels)
}

// addService implements the common parts of AdvertiseService and AdvertiseRemoteService.
func (r *Registry) addService(
	addrPort netip.AddrPort, name string, labels map[string]string,
) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for _, ls := range r.localServices {
		if addrPort == ls.AddrPort {
			return fmt.Errorf("Address %s already registered", addrPort.String())
		}
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	r.localServices = append(r.localServices, Service{
		Name:     name,
		Labels:   labels,
		AddrPort: addrPort,
	})
	logger.Infof(
		"Advertising new service. Name: %s, labels: %v, address: %s",
		name, labels, addrPort.String(),
	)
	return nil
}

// UnlistService removes a local service from the list this registry advertises.
func (r *Registry) UnlistService(port uint16) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	oldLen := len(r.localServices)
	r.localServices = slices.DeleteFunc(r.localServices, func(s Service) bool {
		return port == s.AddrPort.Port()
	})
	if len(r.localServices) == oldLen {
		return fmt.Errorf("No service at port %d", port)
	}
	return nil
}

// Registry HTTP handlers //////////////////////////////////////////////////////

// ServeHTTP provides the HTTP handlers that other Minidisc registries talk to.
func (r *Registry) ServeHTTP(wrt http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/services" {
		r.handleGetServices(wrt, req)
	} else if req.URL.Path == "/add-delegate" {
		r.handlePostAddDelegate(wrt, req)
	} else if req.URL.Path == "/ping" {
		r.handleGetPing(wrt, req)
	} else {
		http.NotFound(wrt, req)
	}
}

// handleGetServices handles "GET /services".
func (r *Registry) handleGetServices(wrt http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		wrt.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Grab local data first.
	r.mutex.Lock()
	services := r.localServices
	delegates := r.delegates
	r.mutex.Unlock()

	// Query delegates sequentially. This assumes that delegates are rare, so
	// querying them in parallel would be unnecessary complexity.
	for _, ap := range delegates {
		if part, err := getRemoteServices(ap); err == nil {
			services = slices.Concat(services, part)
		} else if isUrlError(err) {
			// Errors indicate that the delegate has gone away. Remove it.
			r.removeDelegate(ap)
		}
	}

	// Encode results and send them back.
	wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
	if data, err := json.Marshal(services); err == nil {
		wrt.WriteHeader(http.StatusOK)
		wrt.Write(data)
	} else {
		logger.Errorf("Error generating JSON: %v", err)
		wrt.WriteHeader(http.StatusInternalServerError)
	}
}

type addDelegateRequest struct {
	AddrPort netip.AddrPort `json:"addrPort"`
}

// handlePostAddDelegate handles "POST /add-delegate".
func (r *Registry) handlePostAddDelegate(wrt http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		wrt.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Warnf("Error reading POST body: %v", err)
		wrt.WriteHeader(http.StatusInternalServerError)
		return
	}
	adr := &addDelegateRequest{}
	if err := json.Unmarshal(body, adr); err != nil {
		logger.Warnf("Malformed request: %v", err)
		wrt.WriteHeader(http.StatusBadRequest)
	}
	if adr.AddrPort.Addr() != r.localAddr {
		logger.Warnf("add-delegate request for non-local address %s\n", adr.AddrPort.String())
		wrt.WriteHeader(http.StatusForbidden)
		return
	}
	wrt.WriteHeader(http.StatusOK)

	logger.Infof("Adding delegate at %s", adr.AddrPort)
	r.addDelegate(adr.AddrPort)
}

func (r *Registry) addDelegate(d netip.AddrPort) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for _, ap := range r.delegates {
		if ap == d {
			return // Silently accept double registrations.
		}
	}
	r.delegates = append(r.delegates, d)
}

func (r *Registry) removeDelegate(d netip.AddrPort) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.delegates = slices.DeleteFunc(r.delegates, func(ap netip.AddrPort) bool {
		return ap == d
	})
}

func (r *Registry) handleGetPing(wrt http.ResponseWriter, req *http.Request) {
	wrt.WriteHeader(http.StatusOK)
}

// Minidisc peer-to-peer node management ///////////////////////////////////////

// connect adds this Minidisc registry into the network of registries on the
// Tailnet.
//
// This can result in one of two setups:
//   - If this is the first registry on this host (port 28004 isn't bound), just
//     serve on that port and wait for requests for service listings or for adding
//     delegates.
//   - If port 28004 is already bound, choose an arbitrary port to serve from
//     instead, but then send an add-delegate request to the leader registry at
//     port 28004 so this registry receives service listing requests.
//     Additionally, install a watchdog to detect when the leader registry goes
//     away. If that happens, restart the process to try and become the leader
//     this time.
//
// If port 28004 is already taken by an unrelated server, give up and die.
func (r *Registry) connect() {
	mainAddr := fmt.Sprintf("%s:28004", r.localAddr.String())
	delegateAddr := fmt.Sprintf("%s:0", r.localAddr.String())
	for {
		if listener, err := net.Listen("tcp4", mainAddr); err == nil {
			r.runLeaderNode(listener)
		} else if listener, err := net.Listen("tcp4", delegateAddr); err == nil {
			if err := r.runDelegateNode(listener); err != nil {
				logger.Infof("Waiting 10s before restarting registry")
				time.Sleep(10 * time.Second)
			}
		} else {
			log.Fatalf("Couldn't bind to any port: %v", err)
		}
	}
}

// runLeaderNode runs the HTTP server in "leader" mode.
func (r *Registry) runLeaderNode(listener net.Listener) {
	logger.Infof("Minidisc registry started as leader")
	err := http.Serve(listener, r)
	logger.Infof("Minidisc leader exited: %v", err)
}

// runDelegateNode runs the HTTP server in "delegate" mode. Because we're not
// findable on the main port, we register with the leader node on the same host
// as a delegate. Additionally, we run liveness checks (/ping) every few seconds
// to detect if the leader goes away. When that happens, we shut down the
// delegate server and try to restart it as the leader.
func (r *Registry) runDelegateNode(listener net.Listener) error {
	logger.Infof("Minidisc registry started as leader")
	srv := &http.Server{Handler: r}
	exit := make(chan error)
	go func() {
		exit <- srv.Serve(listener)
	}()

	// Register with leader.
	mainAddr := fmt.Sprintf("%s:28004", r.localAddr.String())
	data, err := json.Marshal(&addDelegateRequest{
		AddrPort: netip.MustParseAddrPort(listener.Addr().String()),
	})
	if err != nil {
		log.Fatalf("Error marshalling JSON: %v", err)
	}
	url := fmt.Sprintf("http://%s/add-delegate", mainAddr)
	mime := "application/json"
	resp, err := http.Post(url, mime, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("Cannot contact leader: %v", err)
	} else if resp.StatusCode != 200 {
		return fmt.Errorf("Error registering with leader: %s", resp.Status)
	}

	// Serve, but regularly check whether the leader has died.
	for {
		select {
		case err := <-exit:
			if err == http.ErrServerClosed {
				logger.Infof("Minidisc delegate exited")
				return nil
			} else {
				logger.Warnf("Minidisc delegate exited with error: %v", err)
				return err
			}
		case <-time.After(5 * time.Second):
			if !r.leaderIsAlive() {
				logger.Infof("Leader is unreachable. Stopping delegate.")
				srv.Shutdown(context.Background())
			}
		}
	}
}

// leaderIsAlive sends a request to the Minidisc leader and returns whether that
// was successful.
func (r *Registry) leaderIsAlive() bool {
	c := http.Client{Timeout: 1 * time.Second}
	url := fmt.Sprintf("http://%s:28004/ping", r.localAddr.String())
	_, err := c.Get(url)
	return err == nil
}

// Tailscale status detection //////////////////////////////////////////////////

type tailnetMap struct {
	LocalAddr netip.Addr
	PeerAddrs []netip.Addr
}

// listTailnetAddrs detects and returns all live IPv4 addresses on the current
// tailnet, including the own host's.
func listTailnetAddrs() ([]netip.Addr, error) {
	tmap, err := getTailnetMap()
	if err != nil {
		return nil, err
	}
	addrs := make([]netip.Addr, 0, 1+len(tmap.PeerAddrs))
	addrs = append(addrs, tmap.LocalAddr)
	addrs = append(addrs, tmap.PeerAddrs...)
	return addrs, nil
}

// Override for testing.
var tailnetMapForTesting *tailnetMap = nil

// getTailnetMap reads the Tailnet status from Tailscale's unix domain socket,
// parses it and returns a map of currently-online IPv4 address on the Tailnet.
//
// Why not just use Tailscale's own library for this, I hear you ask. Indeed,
// the first version of this code did use that library (namely the ipnstate.Status
// struct and related code) but it's not a stable interface and varies between
// versions of the Tailscale code. We could pin the Tailscale dependency to a
// particular version, but that wouldn't play well with clients who depend on
// that library for other reasons. In contrast, this internal socket interface
// is much more stable across versions, and we can even do away with the
// dependency on the Tailscale code.
func getTailnetMap() (tailnetMap, error) {
	if tailnetMapForTesting != nil {
		return *tailnetMapForTesting, nil
	}
	tmap := tailnetMap{}

	// Fake Tailscale's HTTP-over-UDS communication with tailscaled.
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", "/var/run/tailscale/tailscaled.sock")
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   500 * time.Millisecond,
	}
	req, err := http.NewRequest("GET", "http://local-tailscaled.sock/localapi/v0/status", nil)
	if err != nil {
		log.Fatalf("Error constructing http.Request: %v", err)
	}
	req.Host = "local-tailscaled.sock"

	// Send the request.
	resp, err := client.Do(req)
	if err != nil {
		return tmap, fmt.Errorf("Error reading tailnet status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tmap, fmt.Errorf("%s while reading tailnet status", resp.Status)
	}

	// Decode the response.
	var status struct {
		TailscaleIPs []netip.Addr `json:"TailscaleIPs"`
		Peer         map[string]struct {
			Online       bool         `json:"Online"`
			TailscaleIPs []netip.Addr `json:"TailscaleIPs"`
		} `json:"Peer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return tmap, fmt.Errorf("Cannot decode tailnet status: %v", err)
	}
	if addr, ok := findIPv4Addr(status.TailscaleIPs); ok {
		tmap.LocalAddr = addr
	} else {
		return tmap, fmt.Errorf("Cannot find IPv4 Tailscale address for local host")
	}
	for _, peer := range status.Peer {
		if !peer.Online {
			continue
		}
		if addr, ok := findIPv4Addr(peer.TailscaleIPs); ok {
			tmap.PeerAddrs = append(tmap.PeerAddrs, addr)
		}
	}
	return tmap, nil
}

// findIPv4Addr returns the first IPv4 address in the list, or the uninitialised
// address. The bool is true in the former case.
func findIPv4Addr(addrs []netip.Addr) (netip.Addr, bool) {
	for _, addr := range addrs {
		if addr.Is4() {
			return addr, true
		}
	}
	return netip.Addr{}, false
}
