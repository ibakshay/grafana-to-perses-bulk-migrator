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
	inputDir    = flag.String("input-dir", "", "Absolute path to input directory containing Plutono dashboard JSON files to migrate (required)")
	outputDir   = flag.String("output-dir", "", "Absolute path to output directory for migrated files (default: <input-dir>/.migrated)")
	cleanUp     = flag.Bool("cleanup", true, "Cleanup Grafana container after migration (default: false)")
	grafanaPort = flag.String("port", "3000", "Port for Grafana container")
	waitTime    = flag.Duration("wait", 10*time.Second, "Time to wait for Grafana to start (default: 10s)")
	help        = flag.Bool("help", false, "Show help message")
)

func main() {
	flag.Parse()

	if *help {
		flag.Usage()
		return
	}

	if *inputDir == "" {
		log.Fatal("Input directory is required. Use --input-dir flag with absolute path.")
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
		time.Sleep(*waitTime)
	}

	fmt.Printf("Grafana container ready on port %s\n", *grafanaPort)

	if err := migrateDashboards(*inputDir, *grafanaPort); err != nil {
		log.Fatalf("Import failed: %v", err)
	}

	fmt.Printf("Dashboard import completed. All dashboards are now in Grafana.\n")

	if err := exportDashboards(*outputDir, *grafanaPort); err != nil {
		log.Printf("Warning: Failed to export dashboards: %v", err)
	} else {
		fmt.Printf("Dashboard export completed. Updated dashboards saved to %s\n", *outputDir)
	}

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

func exportDashboards(outputDir, port string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %v", err)
	}

	searchURL := fmt.Sprintf("http://admin:admin@localhost:%s/api/search?query=", port)
	resp, err := http.Get(searchURL)
	if err != nil {
		return fmt.Errorf("failed to search dashboards: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("search failed with status %d: %s", resp.StatusCode, string(body))
	}

	var dashboards []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&dashboards); err != nil {
		return fmt.Errorf("failed to decode dashboard list: %v", err)
	}

	fmt.Printf("Found %d dashboards to export\n", len(dashboards))

	for _, dashboard := range dashboards {
		if dashboard["type"] == "dash-db" {
			uid, ok := dashboard["uid"].(string)
			if !ok {
				log.Printf("Warning: Dashboard missing UID, skipping")
				continue
			}

			if err := exportDashboard(uid, outputDir, port); err != nil {
				log.Printf("Warning: Failed to export dashboard %s: %v", uid, err)
				continue
			}
		}
	}

	return nil
}

func exportDashboard(uid, outputDir, port string) error {
	dashboardURL := fmt.Sprintf("http://admin:admin@localhost:%s/api/dashboards/uid/%s", port, uid)
	resp, err := http.Get(dashboardURL)
	if err != nil {
		return fmt.Errorf("failed to get dashboard: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dashboard fetch failed with status %d: %s", resp.StatusCode, string(body))
	}

	var dashboardResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&dashboardResponse); err != nil {
		return fmt.Errorf("failed to decode dashboard response: %v", err)
	}

	meta, ok := dashboardResponse["meta"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("no meta found in dashboard response")
	}

	slug, ok := meta["slug"].(string)
	if !ok {
		slug = uid
	}

	// Extract only the dashboard content (not the full API response)
	dashboard, ok := dashboardResponse["dashboard"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("no dashboard found in response")
	}

	timestamp := time.Now().Format("20060102_1504")
	filename := fmt.Sprintf("%s_%s.json", slug, timestamp)
	filepath := filepath.Join(outputDir, filename)

	dashboardBytes, err := json.MarshalIndent(dashboard, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal dashboard: %v", err)
	}

	if err := os.WriteFile(filepath, dashboardBytes, 0644); err != nil {
		return fmt.Errorf("failed to write dashboard file: %v", err)
	}

	fmt.Printf("  → Exported dashboard: %s\n", filename)
	return nil
}
