package main

import (
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/mscheidegger/minidisc/pkg/minidisc"
	"gopkg.in/yaml.v3"
)

const usage = `Usage: md <command> [parameters]

Available commands:
  list - Print a list of advertised services on the Tailnet.
  find <name> [key=val] ...  - Find a service, given name and labels.
  advertise <cfgfile> - Read service config from YAML and advertise it.
  help - This page.
`

type Config struct {
	Services []Service `yaml:"services"`
}

type Service struct {
	Name    string            `yaml:"name"`
	Address string            `yaml:"address"`
	Labels  map[string]string `yaml:"labels"`
}

func main() {
	if len(os.Args) < 2 {
		help()
		os.Exit(2)
	}
	cmd := os.Args[1]
	params := os.Args[2:len(os.Args)]
	switch cmd {
	case "list":
		list(params)
	case "find":
		find(params)
	case "advertise":
		advertise(params)
	case "help":
		help()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command '%s'\n\n", cmd)
		help()
		os.Exit(2)
	}
}

func help() {
	fmt.Fprintln(os.Stderr, usage)
}

func list(params []string) {
	if len(params) > 0 {
		fmt.Fprintln(os.Stderr, "'list' doesn't take parameters")
		os.Exit(2)
	}
	ss, err := minidisc.ListServices()
	if err != nil {
		log.Fatal(err)
	}
	if len(ss) == 0 {
		fmt.Fprintln(os.Stderr, "No advertised services found")
		return
	}
	tw := tabwriter.NewWriter(
		os.Stdout,
		0,   // minwidth
		0,   // tabwidth
		3,   // padding
		' ', // padchar
		0,   // options
	)
	for _, s := range ss {
		labels := fmtLabels(s.Labels)
		fmt.Fprintf(tw, "* %s\t%s\t%s\t\n", s.Name, s.AddrPort.String(), labels)
	}
	tw.Flush()
}

func fmtLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	var parts []string
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	slices.Sort(parts)
	return fmt.Sprintf("{ %s }", strings.Join(parts, ", "))
}

func find(params []string) {
	if len(params) < 1 {
		fmt.Fprintln(os.Stderr, "'find' takes at least 1 parameter")
		os.Exit(2)
	}
	name := params[0]
	labels := make(map[string]string)
	for _, p := range params[1:len(params)] {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Cannot parse label '%s'\n", p)
			os.Exit(2)
		}
		labels[parts[0]] = parts[1]
	}
	if addr, err := minidisc.FindService(name, labels); err == nil {
		fmt.Println(addr.String())
	} else {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
}

func advertise(params []string) {
	if len(params) != 1 {
		fmt.Fprintln(os.Stderr, "'advertise' takes exactly 1 parameter")
		os.Exit(2)
	}
	path := params[0]
	if path == "-" {
		path = "/dev/stdin"
	}

	cfg, err := readConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config file: %v\n", err)
		os.Exit(2)
	}

	// Start and fill registry.
	registry, err := minidisc.StartRegistry()
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range cfg.Services {
		if strings.HasPrefix(s.Address, ":") {
			port := parsePort(s.Address)
			if err := registry.AdvertiseService(port, s.Name, s.Labels); err != nil {
				log.Fatal(err)
			}
		} else {
			ap, err := netip.ParseAddrPort(s.Address)
			if err != nil {
				log.Fatalf("Bad address '%s'", s.Address)
			}
			if err := registry.AdvertiseRemoteService(ap, s.Name, s.Labels); err != nil {
				log.Fatal(err)
			}
		}
	}

	// Wait for a signal before terminating.
	log.Println("Advertising services. Stop by sending SIGINT...")
	quit := make(chan os.Signal)
	signal.Notify(quit, os.Interrupt)
	<-quit
}

func readConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func parsePort(addr string) uint16 {
	addr = addr[1:len(addr)] // Remove leading :
	port, err := strconv.ParseUint(addr, 10, 16)
	if err != nil {
		log.Fatalf("Bad address '%s'", addr)
	}
	return uint16(port)
}
