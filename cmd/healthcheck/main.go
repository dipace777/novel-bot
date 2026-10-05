// healthcheck provides a bounded HTTP readiness probe for minimal container images.
package main

import (
	"flag"
	"net/http"
	"os"
	"time"
)

func main() {
	private := flag.Bool("worker", false, "Use the private worker credential")
	flag.Parse()
	if flag.NArg() != 1 {
		os.Exit(2)
	}
	req, err := http.NewRequest(http.MethodGet, flag.Arg(0), nil)
	if err != nil {
		os.Exit(2)
	}
	if *private {
		req.Header.Set("Authorization", "Bearer "+os.Getenv("WORKER_AUTH_TOKEN"))
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		os.Exit(1)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
