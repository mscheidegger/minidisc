package minidisc

import (
	//"bytes"
	//"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	//"tailscale.com/ipn/ipnstate"
	//"tailscale.com/types/key"
)

var (
	fakeTailnetMap *tailnetMap        = nil
	testServers    []*httptest.Server = nil
	registry       *Registry          = nil
)

func TestMain(m *testing.M) {
	setupEnv()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func setupEnv() {
	fakeTailnetMap = &tailnetMap{}
	tailnetMapForTesting = fakeTailnetMap
	setupRegistry()
	setupDelegate()
	setupPeers()
}

func setupRegistry() {
	fakeTailnetMap.LocalAddr = netip.MustParseAddr("127.0.0.2")
	var err error
	registry, err = StartRegistry()
	if err != nil {
		log.Fatal(err)
	}
	if err := registry.AdvertiseService(42, "foo", nil); err != nil {
		log.Fatal(err)
	}
}

func setupDelegate() {
	// This is essentially the same as setupRegistry() but runs after, so the
	// registry will end up as delegate. This is non-deterministic - sleep a
	// little to get this closer to determinism.
	time.Sleep(12 * time.Millisecond)
	registry, err := StartRegistry()
	if err != nil {
		log.Fatal(err)
	}
	if err := registry.AdvertiseService(24, "oof", nil); err != nil {
		log.Fatal(err)
	}
}

func setupPeers() {
	peers := []struct {
		service string
		addr    string
	}{
		{"bar", "127.0.0.3"},
		{"baz", "127.0.0.4"},
	}
	var servers []*httptest.Server
	for _, p := range peers {
		parsedAddr := netip.MustParseAddr(p.addr)
		fakeTailnetMap.PeerAddrs = append(fakeTailnetMap.PeerAddrs, parsedAddr)
		ln, err := net.Listen("tcp", p.addr+":28004")
		if err != nil {
			log.Fatal(err)
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(
				w, `[{"name":"%s","labels":{},"addrPort":"%s:42"}]`,
				p.service, p.addr,
			)
		})
		srv := httptest.NewUnstartedServer(handler)
		srv.Listener = ln
		srv.Start()
		servers = append(servers, srv)
	}
}

func cleanup() {
	for _, srv := range testServers {
		srv.CloseClientConnections()
		srv.Close()
	}
}

// Test serviceMatches behavior
func TestServiceMatches(t *testing.T) {
	base := Service{
		Name: "svc",
		Labels: map[string]string{
			"env": "prod",
			"v":   "1",
		},
	}
	other := Service{
		Name:   "other",
		Labels: base.Labels,
	}

	cases := []struct {
		title string
		s     Service
		want  bool
		lbls  map[string]string
	}{
		{"match exact", base, true, map[string]string{"env": "prod", "v": "1"}},
		{"subset labels", base, true, map[string]string{"env": "prod"}},
		{"extra label", base, false, map[string]string{"env": "prod", "x": "y"}},
		{"name mismatch", other, false, nil},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			got := serviceMatches(c.s, "svc", c.lbls)
			if got != c.want {
				t.Errorf("serviceMatches() = %v, want %v", got, c.want)
			}
		})
	}
}

// Test isUrlError on different error types
func TestIsUrlError(t *testing.T) {
	uerr := &url.Error{Op: "Get", URL: "http://x", Err: errors.New("fail")}
	if !isUrlError(uerr) {
		t.Error("isUrlError should return true for *url.Error")
	}
	if isUrlError(errors.New("generic")) {
		t.Error("isUrlError should return false for generic errors")
	}
}

func TestListServices(t *testing.T) {
	ss, err := ListServices()
	if err != nil {
		t.Errorf("ListServices failed: %v", err)
	}
	expected := []Service{
		{"foo", map[string]string{}, netip.MustParseAddrPort("127.0.0.2:42")},
		{"oof", map[string]string{}, netip.MustParseAddrPort("127.0.0.2:24")},
		{"bar", map[string]string{}, netip.MustParseAddrPort("127.0.0.3:42")},
		{"baz", map[string]string{}, netip.MustParseAddrPort("127.0.0.4:42")},
	}
	sFunc := func(a, b Service) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(ss, sFunc)
	slices.SortFunc(expected, sFunc)
	if !reflect.DeepEqual(ss, expected) {
		t.Errorf("Wrong ListServices results.\nExpected: %v\nActual: %v", expected, ss)
	}
}

func TestFindService(t *testing.T) {
	ap, err := FindService("baz", nil)
	if err != nil {
		t.Errorf("FindService failed: %v", err)
	}
	expected := netip.MustParseAddrPort("127.0.0.4:42")
	if ap != expected {
		t.Errorf("Expected service address %s, got %s", expected, ap)
	}
}

func TestServiceManagement(t *testing.T) {
	_, err := FindService("findme", map[string]string{"env": "prod"})
	if err == nil {
		t.Errorf("Found non-existent service 'findme'")
	}

	registry.AdvertiseService(1234, "findme", map[string]string{"env": "prod", "x": "y"})
	ap, err := FindService("findme", map[string]string{"env": "prod"})
	if err != nil {
		t.Errorf("FindService should have found 'findme': %v", err)
	}
	expected := netip.MustParseAddrPort("127.0.0.2:1234")
	if ap != expected {
		t.Errorf("Expected address %s, got %s", expected, ap)
	}

	registry.UnlistService(1234)
	_, err = FindService("findme", map[string]string{"env": "prod"})
	if err == nil {
		t.Errorf("Found unlisted service 'findme'")
	}
}
