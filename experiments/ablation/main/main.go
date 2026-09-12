// Command ablation runs the enrichment ablation matrix.
package main

import (
	"fmt"
	"os"

	"github.com/Alpenl/cairn-x-enricher/experiments/ablation"
)

func main() {
	if err := ablation.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
