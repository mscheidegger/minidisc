package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/mscheidegger/minidisc/pkg/minidisc"
)

const usage = `Usage: md <command> [parameters]

Available commands:
  list - Print a list of advertised services on the Tailnet.
  find <name> [key=val] ...  - Find a service, given name and labels.
  advertise <json file> - Read service config from JSON and advertise it.
  help - This page.
`

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

	// Read services from config file.
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't read '%s': %v\n", path, err)
		os.Exit(2)
	}
	var ss []minidisc.Service
	if err := json.Unmarshal(data, &ss); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config file: %v\n", err)
		os.Exit(2)
	}

	// Start and fill registry.
	registry, err := minidisc.StartRegistry()
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range ss {
		// TODO: This should maybe allow advertising for different addresses
		// than the local host's.
		port := s.AddrPort.Port()
		if err := registry.AdvertiseService(port, s.Name, s.Labels); err != nil {
			log.Fatal(err)
		}
	}

	// Wait for a signal before terminating.
	log.Println("Advertising services. Stop by sending SIGINT...")
	quit := make(chan os.Signal)
	signal.Notify(quit, os.Interrupt)
	<-quit
}
