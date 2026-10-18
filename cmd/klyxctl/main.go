// Command klyxctl is a headless smoke tool for the Klyx data foundation.
// Usage: klyxctl --config path/to/fleet.yaml [--wait 5s]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/secretstore"
)

func main() {
	cfgPath := flag.String("config", "", "path to Klyx fleet config")
	wait := flag.Duration("wait", 5*time.Second, "how long to let connections sync")
	flag.Parse()

	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		os.Exit(2)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := fleet.NewRegistry(cfg, fleet.DefaultConnFactory(clock.Real{}, fleet.Credentials{Store: secretstore.OS{}, Revisions: endpointcred.NewRegistry()}))
	reg.Start(ctx)

	time.Sleep(*wait) // give informers time to sync for this one-shot tool

	snaps := reg.Snapshots()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// Writes to a stdout-backed tabwriter; the errors are not actionable in a CLI.
	_, _ = fmt.Fprintln(w, "CLUSTER\tSTATE\tNODES\tPODS\tGITOPS\tNETWORK\tREASON")
	for _, s := range snaps {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d/%d\t%d\t%s\t%s\t%s\n",
			s.Name, s.State,
			s.NodesReady, s.NodesTotal, s.Pods,
			s.Capabilities.GitOps.Tier, s.Capabilities.Network.Tier,
			s.Reason)
	}
	_ = w.Flush()

	sum := fleet.Summarize(snaps)
	fmt.Printf("\nfleet: %d/%d answered, %d/%d nodes ready, %d pods, partial=%v\n",
		sum.Answered, sum.TotalClusters, sum.NodesReady, sum.NodesTotal, sum.TotalPods, sum.Partial)
}
