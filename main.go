package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	inputDir      = flag.String("input-dir", "", "Absolute path to input directory containing Plutono dashboard JSON files to migrate (required)")
	outputDir     = flag.String("output-dir", "", "Absolute path to output directory for migrated files (default: <input-dir>/.migrated)")
	cleanUp       = flag.Bool("cleanup", false, "Cleanup containers after migration (default: false)")
	grafanaPort   = flag.String("grafana-port", "3000", "Port for Grafana container")
	persesPort    = flag.String("perses-port", "8080", "Port for Perses container")
	waitTime      = flag.Duration("wait", 10*time.Second, "Time to wait for containers to start (default: 10s)")
	persesVersion = flag.String("perses-version", "0.52.0-beta.3", "Version of percli to download (default: 0.52.0-beta.3)")
	help          = flag.Bool("help", false, "Show help message")
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

	// Setup cleanup defer function
	defer func() {
		if *cleanUp {
			if err := deleteContainer(*grafanaPort); err != nil {
				log.Printf("Warning: Failed to delete Grafana container: %v", err)
			} else {
				fmt.Println("Grafana container deleted successfully")
			}

			if err := deleteContainer(*persesPort); err != nil {
				log.Printf("Warning: Failed to delete Perses container: %v", err)
			} else {
				fmt.Println("Perses container deleted successfully")
			}
		}
	}()

	if err := startGrafanaContainer(*grafanaPort); err != nil {
		log.Fatalf("Failed to setup Grafana container: %v", err)
	}

	if err := startPersesContainer(*persesPort); err != nil {
		log.Fatalf("Failed to setup Perses container: %v", err)
	}

	if err := migrateDashboards(*inputDir, *grafanaPort); err != nil {
		log.Fatalf("Import failed: %v", err)
	}

	fmt.Printf("Dashboard import completed. All dashboards are now in Grafana.\n")

	if err := exportDashboards(*outputDir, *grafanaPort, *inputDir); err != nil {
		log.Printf("Warning: Failed to export dashboards: %v", err)
	} else {
		fmt.Printf("Dashboard export completed. Updated dashboards saved to %s\n", *outputDir)
	}

	if err := downloadPercli(); err != nil {
		log.Printf("Warning: Failed to download percli: %v", err)
	}
}

func startContainer(name, image, hostPort, containerPort string) error {

	fmt.Printf("Starting %s container on port %s...\n", name, hostPort)

	cmd := exec.Command("docker", "run", "-d", "--name", name, "-p", fmt.Sprintf("%s:%s", hostPort, containerPort), image)
	return cmd.Run()
}

func isContainerRunning(port string) (bool, error) {
	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("publish=%s", port), "--format", "{{.ID}}")
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}

func deleteContainer(port string) error {
	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("publish=%s", port), "--format", "{{.ID}}")
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	if len(output) > 0 {
		containerID := strings.TrimSpace(string(output))
		deleteCmd := exec.Command("docker", "rm", "-f", containerID)
		return deleteCmd.Run()
	}

	return nil
}

func startGrafanaContainer(port string) error {
	running, err := isContainerRunning(port)
	if err != nil {
		return fmt.Errorf("failed to check if Grafana container is running: %v", err)
	}

	if !running {
		if err := startContainer("grafana", "grafana/grafana", port, "3000"); err != nil {
			return fmt.Errorf("failed to start Grafana container: %v", err)
		}
		fmt.Printf("Waiting for Grafana to start...\n")
		time.Sleep(*waitTime)
	}

	fmt.Printf("Grafana container ready on port %s\n", port)
	return nil
}

func startPersesContainer(port string) error {
	running, err := isContainerRunning(port)
	if err != nil {
		return fmt.Errorf("failed to check if Perses container is running: %v", err)
	}

	if !running {
		if err := startContainer("perses", "persesdev/perses", port, "8080"); err != nil {
			return fmt.Errorf("failed to start Perses container: %v", err)
		}
		fmt.Printf("Waiting for Perses to start...\n")
		time.Sleep(*waitTime)
	}

	fmt.Printf("Perses container ready on port %s\n", port)
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
		fmt.Printf("Processing file: %s\n", file)
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

	// Log original dashboard info
	originalID := dashboard["id"]
	originalUID := dashboard["uid"]
	title := dashboard["title"]
	fmt.Printf("  → Dashboard title: %v, original ID: %v, original UID: %v\n", title, originalID, originalUID)

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

	fmt.Printf("  → Import response status: %s\n", importResponse["status"])

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

func exportDashboards(outputDir, port, inputDir string) error {
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

	fmt.Printf("\nExporting the dashboards to path %s:\n", outputDir)
	exportCount := 0
	dashboardIndex := 0
	for _, dashboard := range dashboards {
		if dashboard["type"] == "dash-db" {
			dashboardIndex++
			uid, ok := dashboard["uid"].(string)
			if !ok {
				log.Printf("Warning: Dashboard missing UID, skipping: %v", dashboard["title"])
				continue
			}

			fmt.Printf("  [%d] Title: %v, UID: %v\n", dashboardIndex, dashboard["title"], uid)
			if err := exportDashboard(uid, outputDir, port); err != nil {
				log.Printf("Warning: Failed to export dashboard %s: %v", uid, err)
				continue
			}
			exportCount++
		}
	}

	fmt.Printf("Successfully exported %d dashboards\n", exportCount)

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

func downloadPercli() error {
	binPath := "./bin/percli"

	// Check if percli already exists
	if _, err := os.Stat(binPath); err == nil {
		fmt.Printf("percli binary already exists at %s, skipping download\n", binPath)
		return nil
	}

	fmt.Println("Downloading percli binary...")

	// Determine OS and architecture
	osName := runtime.GOOS
	arch := runtime.GOARCH
	fmt.Printf("Detected platform: %s/%s\n", osName, arch)

	// Handle special case for arm architecture
	if arch == "arm" {
		arch = "armv6"
	}

	// Handle Windows extension
	executableName := "percli"
	if osName == "windows" {
		executableName = "percli.exe"
	}

	// Construct download URL
	downloadURL := fmt.Sprintf("https://github.com/perses/perses/releases/download/v%s/perses_%s_%s_%s.tar.gz", *persesVersion, *persesVersion, osName, arch)

	// Download the tar.gz file
	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download percli: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Create gzip reader
	gzipReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %v", err)
	}
	defer gzipReader.Close()

	// Create tar reader
	tarReader := tar.NewReader(gzipReader)

	// Create bin directory if it doesn't exist
	if err := os.MkdirAll("./bin", 0755); err != nil {
		return fmt.Errorf("failed to create bin directory: %v", err)
	}

	// Extract percli binary from tar
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %v", err)
		}

		// Look for the percli executable
		if filepath.Base(header.Name) == executableName {
			// Create the output file
			outFile, err := os.Create(binPath)
			if err != nil {
				return fmt.Errorf("failed to create output file: %v", err)
			}
			defer outFile.Close()

			// Copy the file content
			if _, err := io.Copy(outFile, tarReader); err != nil {
				return fmt.Errorf("failed to extract percli: %v", err)
			}

			// Make it executable
			if err := os.Chmod(binPath, 0755); err != nil {
				return fmt.Errorf("failed to make percli executable: %v", err)
			}

			fmt.Printf("Successfully downloaded percli to %s\n", binPath)
			return nil
		}
	}

	return fmt.Errorf("percli binary not found in the downloaded archive")
}
