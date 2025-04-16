package main

import (
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/mscheidegger/minidisc/pkg/minidisc"
)

func main() {
	ss, err := minidisc.ListServices()
	if err != nil {
		log.Fatal(err)
	}
	if len(ss) == 0 {
		fmt.Println("No advertised services found")
	} else {
		tw := tabwriter.NewWriter(
			os.Stdout,
			0, // minwidth
			0, // tabwidth
			3, // padding
			' ', // padchar
			0, // options
		)
		for _, s := range ss {
			labels := fmtLabels(s.Labels)
			fmt.Fprintf(tw, "* %s\t%s\t%s\t\n", s.Name, s.AddrPort.String(), labels)
			//fmt.Printf("* %s | %s | %s\n", s.Name, s.AddrPort.String(), labels)
		}
		tw.Flush()
	}
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
