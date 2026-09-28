// Command testserver runs the deterministic test server standalone
// (used by E2E runs and manual testing of the TestApp).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/droidpector/apkinspector/src/testserver"
)

func main() {
	httpAddr := flag.String("http", "127.0.0.1:18080", "plain HTTP listen address")
	httpsAddr := flag.String("https", "127.0.0.1:18443", "HTTPS listen address")
	names := flag.String("names", "test.apkinspector.internal", "comma-separated TLS host names")
	caOut := flag.String("ca-out", "", "write the test CA certificate (PEM) to this file")
	flag.Parse()

	s, err := testserver.Start(*httpAddr, *httpsAddr, strings.Split(*names, ","))
	if err != nil {
		fmt.Fprintln(os.Stderr, "testserver:", err)
		os.Exit(1)
	}
	defer s.Close()
	if *caOut != "" {
		if err := os.WriteFile(*caOut, s.CAPEM, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "testserver:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("testserver http=%s https=%s\n", s.HTTPAddr, s.HTTPSAddr)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
