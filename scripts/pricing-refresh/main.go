package main

import (
	"flag"
	"fmt"
	"os"

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
	if *out == "internal/pricing/catalog.json" {
		panic("refusing to overwrite the reviewed runtime catalog")
	}
	if err := os.WriteFile(*out, canonical, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote review candidate %s; compare every rate and provenance field with the primary source\n", *out)
}
