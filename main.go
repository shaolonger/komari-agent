package main

import (
	"os"

	"github.com/komari-monitor/komari-agent/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
