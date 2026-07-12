package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agensfield/scriba/internal/pricing"
)

func main() {
	source := flag.String("source", "internal/pricing/catalog.json", "downloaded or manually prepared catalog")
	out := flag.String("out", "internal/pricing/catalog.candidate.json", "candidate output (never the runtime catalog)")
	flag.Parse()
	data, err := os.ReadFile(*source)
	if err != nil {
		panic(err)
	}
	canonical, err := pricing.CheckCatalog(data)
	if err != nil {
		panic(err)
	}
	runtimeCatalog, err := filepath.Abs("internal/pricing/catalog.json")
	if err != nil {
		panic(err)
	}
	candidate, err := filepath.Abs(*out)
	if err != nil {
		panic(err)
	}
	if candidate == runtimeCatalog {
		panic("refusing to overwrite the reviewed runtime catalog")
	}
	// #nosec G703 -- the maintainer explicitly selects the candidate path; the runtime catalog is excluded above.
	if err := os.WriteFile(candidate, canonical, 0o600); err != nil {
		panic(err)
	}
	fmt.Printf("wrote review candidate %s; compare every rate and provenance field with the primary source\n", candidate)
}
