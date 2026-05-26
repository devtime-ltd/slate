package main

import (
	"os"

	"github.com/devtime-ltd/slate/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
