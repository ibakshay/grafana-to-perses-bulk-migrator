package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	inputDir    = flag.String("dir", "", "Absolute path to directory containing Plutono dashboard JSON files to migrate (required)")
	outputDir   = flag.String("output", "", "Absolute path to output directory for migrated files (default: <input-dir>/.migrated)")
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

	if *inputDir == "" {
		log.Fatal("Input directory is required. Use -dir flag with absolute path.")
	}

	if *outputDir == "" {
		*outputDir = filepath.Join(*inputDir, ".migrated")
	}

	running, err := isGrafanaContainerRunning(*grafanaPort)
	if err != nil {
		log.Fatalf("Failed to check if Grafana container is running: %v", err)
	}

	if !running {
		if err := startGrafanaContainer(*grafanaPort); err != nil {
			log.Fatalf("Failed to start Grafana container: %v", err)
		}
		fmt.Printf("Waiting for Grafana to start...\n")
		time.Sleep(10 * time.Second)
	}

	fmt.Printf("Grafana container ready on port %s\n", *grafanaPort)

	if err := migrateDashboards(*inputDir, *grafanaPort); err != nil {
		log.Fatalf("Import failed: %v", err)
	}

	fmt.Printf("Dashboard import completed. All dashboards are now in Grafana.\n")

	if *cleanUp {
		if err := stopGrafanaContainer(*grafanaPort); err != nil {
			log.Printf("Warning: Failed to cleanup Grafana container: %v", err)
		} else {
			fmt.Println("Grafana container cleaned up successfully")
		}
	}
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

func stopGrafanaContainer(port string) error {
	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("publish=%s", port), "--format", "{{.ID}}")
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	if len(output) > 0 {
		containerID := strings.TrimSpace(string(output))
		stopCmd := exec.Command("docker", "stop", containerID)
		return stopCmd.Run()
	}

	return nil
}

func migrateDashboards(inputDir, port string) error {
	files, err := filepath.Glob(filepath.Join(inputDir, "*.json"))
	if err != nil {
		return fmt.Errorf("failed to find JSON files: %v", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no JSON files found in directory: %s", inputDir)
	}

	fmt.Printf("Found %d JSON files to import\n", len(files))

	for _, file := range files {
		fmt.Printf("Importing: %s\n", filepath.Base(file))

		if err := migrateDashboard(file, port); err != nil {
			log.Printf("Warning: Failed to import %s: %v", filepath.Base(file), err)
			continue
		}

		fmt.Printf("Successfully imported: %s\n", filepath.Base(file))
	}

	return nil
}

func migrateDashboard(inputFile, port string) error {
	dashboardData, err := os.ReadFile(inputFile)
	if err != nil {
		return fmt.Errorf("failed to read dashboard file: %v", err)
	}

	var dashboard map[string]interface{}
	if err := json.Unmarshal(dashboardData, &dashboard); err != nil {
		return fmt.Errorf("failed to parse dashboard JSON: %v", err)
	}

	// Remove ID and UID to allow Grafana to assign new ones
	delete(dashboard, "id")
	delete(dashboard, "uid")

	// Import the dashboard to Grafana (which migrates the schema)
	importPayload := map[string]interface{}{
		"dashboard": dashboard,
		"overwrite": true,
	}

	payloadBytes, err := json.Marshal(importPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal import payload: %v", err)
	}

	importURL := fmt.Sprintf("http://admin:admin@localhost:%s/api/dashboards/db", port)
	resp, err := http.Post(importURL, "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to import dashboard: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("import failed with status %d: %s", resp.StatusCode, string(body))
	}

	var importResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&importResponse); err != nil {
		return fmt.Errorf("failed to decode import response: %v", err)
	}

	// Get the dashboard info from import response
	dashboardUID, ok := importResponse["uid"].(string)
	if !ok {
		return fmt.Errorf("no UID found in import response")
	}

	dashboardID, ok := importResponse["id"].(float64)
	if !ok {
		return fmt.Errorf("no ID found in import response")
	}

	fmt.Printf("  → Imported dashboard: ID=%d, UID=%s\n", int(dashboardID), dashboardUID)
	return nil
}
