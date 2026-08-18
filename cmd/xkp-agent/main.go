package main

import (
	"flag"
	"fmt"
	"os"

	"xkp-agent/internal/config"
)

func main() {
	configPath := flag.String("config", "/etc/xkp-agent/config.yaml", "configuration file")
	flag.Parse()
	if _, err := config.Load(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "xkp-agent configuration error:", err)
		os.Exit(1)
	}
	fmt.Println("xkp-agent configuration loaded")
}
