package main

import (
	"os"

	"github.com/agensfield/scriba/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
