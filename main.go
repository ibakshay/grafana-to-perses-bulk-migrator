package main

import (
	"flag"
	"fmt"
	"log"
	"os/exec"
)

var (
	inputDir    = flag.String("dir", "", "Directory containing Plutono dashboard JSON files to migrate (required)")
	outputDir   = flag.String("output", "", "Output directory for migrated files (default: <input-dir>/.migrated)")
	cleanUp     = flag.Bool("cleanup", false, "Cleanup Grafana container after migration (default: false)")
	grafanaPort = flag.String("port", "3000", "Port for Grafana container")
	help        = flag.Bool("help", false, "Show help message")
)

func main() {

	flag.Parse()

	if *help {
		flag.Usage()
		return
	}

	running, err := isGrafanaContainerRunning(*grafanaPort)
	if err != nil {
		log.Fatalf("Failed to check if Grafana container is running: %v", err)
	}

	if !running {
		if err := startGrafanaContainer(*grafanaPort); err != nil {
			log.Fatalf("Failed to start Grafana container: %v", err)

		}
	}

	fmt.Printf("Grafana container ready on port %s\n", *grafanaPort)
}

func startGrafanaContainer(port string) error {

	fmt.Printf("Starting Grafana container on port %s...\n", port)

	cmd := exec.Command("docker", "run", "-d", "-p", fmt.Sprintf("%s:3000", port), "grafana/grafana")
	return cmd.Run()
}

func isGrafanaContainerRunning(port string) (bool, error) {
	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("publish=%s", port), "--format", "{{.ID}}")
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}
