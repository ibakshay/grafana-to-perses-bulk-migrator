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
	cleanUp       = flag.Bool("cleanup", true, "Cleanup containers after migration (default: false)")
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

	dashboardUIDs, err := updateGrafanaSchemasToLatestVersion(*inputDir, *grafanaPort)
	if err != nil {
		log.Fatalf("Import failed: %v", err)
	}

	fmt.Printf("✓ Schema update completed\n\n")

	grafanaOutputDir := filepath.Join(*outputDir, "grafana-schema-latest")
	if err := exportUpdatedGrafanaDashboards(dashboardUIDs, grafanaOutputDir, *grafanaPort); err != nil {
		log.Printf("Warning: Failed to export dashboards: %v", err)
	}

	fmt.Println("Setting up Perses migration tools...")
	if err := downloadPercli(); err != nil {
		log.Fatalf("Failed to download percli: %v", err)
	}

	if err := loginPercli(); err != nil {
		log.Fatalf("Failed to login to Perses: %v", err)
	}

	fmt.Println("\nMigrating Grafana dashboards to Perses Schema format...")
	persesOutputDir := filepath.Join(*outputDir, "perses")
	if err := migrateDashboardsToPerses(grafanaOutputDir, persesOutputDir); err != nil {
		log.Printf("Warning: Failed to migrate dashboards to Perses: %v", err)
	} else {
		fmt.Printf("\n🎉 Migration completed!\n")
		fmt.Printf("📁 Perses dashboards are available at: %s\n", persesOutputDir)
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

func updateGrafanaSchemasToLatestVersion(inputDir, port string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(inputDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to find JSON files: %v", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no JSON files found in directory: %s", inputDir)
	}

	fmt.Printf("Updating schemas for %d dashboards...\n", len(files))

	var dashboardUIDs []string
	for _, file := range files {
		fmt.Printf("Processing file: %s\n", file)
		fmt.Printf("Importing: %s\n", filepath.Base(file))
		uid, err := importDashboardToGrafana(file, port)
		if err != nil {
			log.Printf("Warning: Failed to import %s: %v", filepath.Base(file), err)
			continue
		}

		fmt.Printf("Successfully imported: %s\n", filepath.Base(file))
		dashboardUIDs = append(dashboardUIDs, uid)
	}

	return dashboardUIDs, nil
}

func importDashboardToGrafana(inputFile, port string) (string, error) {
	// Import dashboard into Grafana to automatically update its schema to the latest version
	// Grafana normalizes the dashboard format on import, ensuring compatibility with Perses migration
	dashboardData, err := os.ReadFile(inputFile)
	if err != nil {
		return "", fmt.Errorf("failed to read dashboard file: %v", err)
	}

	var dashboard map[string]interface{}
	if err := json.Unmarshal(dashboardData, &dashboard); err != nil {
		return "", fmt.Errorf("failed to parse dashboard JSON: %v", err)
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
		return "", fmt.Errorf("failed to marshal import payload: %v", err)
	}

	importURL := fmt.Sprintf("http://admin:admin@localhost:%s/api/dashboards/db", port)
	resp, err := http.Post(importURL, "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("failed to import dashboard: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("import failed with status %d: %s", resp.StatusCode, string(body))
	}

	var importResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&importResponse); err != nil {
		return "", fmt.Errorf("failed to decode import response: %v", err)
	}

	fmt.Printf("  → Import response status: %s\n", importResponse["status"])

	// Get the dashboard info from import response
	dashboardUID, ok := importResponse["uid"].(string)
	if !ok {
		return "", fmt.Errorf("no UID found in import response")
	}

	dashboardID, ok := importResponse["id"].(float64)
	if !ok {
		return "", fmt.Errorf("no ID found in import response")
	}

	fmt.Printf("  → Imported dashboard: ID=%d, UID=%s\n", int(dashboardID), dashboardUID)
	return dashboardUID, nil
}

func exportUpdatedGrafanaDashboards(dashboardUIDs []string, outputDir, port string) error {
	// Export dashboards from Grafana using collected UIDs from import process
	// This gives us the latest Grafana schema format required for successful Perses migration
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %v", err)
	}

	fmt.Printf("Found %d dashboards to export\n", len(dashboardUIDs))

	fmt.Printf("\nExporting dashboards with updated schemas to path %s:\n This is necessary because Perses migration requires the latest Grafana schema format. \n", outputDir)
	exportCount := 0
	for i, uid := range dashboardUIDs {
		fmt.Printf("  [%d] UID: %s\n", i+1, uid)
		if err := exportSingleUpdatedDashboard(uid, outputDir, port); err != nil {
			log.Printf("Warning: Failed to export dashboard %s: %v", uid, err)
			continue
		}
		exportCount++
	}

	fmt.Printf("Successfully exported %d dashboards\n", exportCount)

	return nil
}

func exportSingleUpdatedDashboard(uid, outputDir, port string) error {
	dashboardURL := fmt.Sprintf("http://admin:admin@localhost:%s/apis/dashboard.grafana.app/v1beta1/namespaces/default/dashboards/%s", port, uid)
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

	// Extract spec which contains the dashboard definition
	spec, ok := dashboardResponse["spec"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("no spec found in dashboard response")
	}

	// Add uid field at root level for Perses dashboard name generation
	// Use the original UID which already follows the correct format
	spec["uid"] = uid

	// Use the UID as the base filename
	slug := uid
	if title, ok := spec["title"].(string); ok && title != "" {
		// Clean the title to make it filename-safe
		slug = strings.ReplaceAll(title, " ", "_")
		slug = strings.ReplaceAll(slug, "/", "_")
	}

	timestamp := time.Now().Format("20060102_1504")
	filename := fmt.Sprintf("%s_%s.json", slug, timestamp)
	filepath := filepath.Join(outputDir, filename)

	// Export the spec (dashboard definition) as JSON
	dashboardBytes, err := json.MarshalIndent(spec, "", "  ")
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

func loginPercli() error {
	binPath := "./bin/percli"

	// Check if percli binary exists
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		return fmt.Errorf("percli binary not found at %s", binPath)
	}

	// Construct login URL with dynamic port
	loginURL := fmt.Sprintf("http://localhost:%s", *persesPort)
	fmt.Printf("Logging into Perses at %s...\n", loginURL)

	// Run percli login command
	cmd := exec.Command(binPath, "login", loginURL, "-u", "admin", "-p", "password")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to login to Perses: %v, output: %s", err, string(output))
	}

	fmt.Printf("✓ Logged into Perses\n")
	return nil
}

func migrateDashboardsToPerses(grafanaOutputDir, persesOutputDir string) error {
	binPath := "./bin/percli"

	// Check if percli binary exists
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		return fmt.Errorf("percli binary not found at %s", binPath)
	}

	// Create perses output directory
	if err := os.MkdirAll(persesOutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create perses output directory: %v", err)
	}

	// Find all JSON files in grafana output directory
	files, err := filepath.Glob(filepath.Join(grafanaOutputDir, "*.json"))
	if err != nil {
		return fmt.Errorf("failed to find JSON files in grafana output directory: %v", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no JSON files found in grafana output directory: %s", grafanaOutputDir)
	}

	fmt.Printf("Found %d Grafana dashboards to migrate to Perses\n", len(files))
	fmt.Printf("\nMigrating dashboards to Perses Schema format:\n")

	migratedCount := 0
	for i, file := range files {
		fmt.Printf("  [%d/%d] Migrating: %s\n", i+1, len(files), filepath.Base(file))

		// Construct output file path in perses directory
		outputFile := filepath.Join(persesOutputDir, filepath.Base(file))

		// Run percli migrate command
		cmd := exec.Command(binPath, "migrate", "--online", "-f", file, "-o", "json")
		output, err := cmd.Output()
		if err != nil {
			log.Printf("Warning: Failed to migrate %s: %v", filepath.Base(file), err)
			continue
		}

		// Clean up datasource references to use default datasource
		cleanedOutput, err := removeDatasourceNames(output)
		if err != nil {
			log.Printf("Warning: Failed to clean datasource references in %s: %v", filepath.Base(file), err)
			cleanedOutput = output // Use original output if cleanup fails
		}

		// Save the migrated dashboard to perses output directory
		if err := os.WriteFile(outputFile, cleanedOutput, 0644); err != nil {
			log.Printf("Warning: Failed to save migrated dashboard %s: %v", filepath.Base(file), err)
			continue
		}

		fmt.Printf("    → Successfully migrated to: %s\n", filepath.Base(outputFile))
		migratedCount++
	}

	fmt.Printf("Successfully migrated %d/%d dashboards to Perses Schema format\n", migratedCount, len(files))
	return nil
}

func removeDatasourceNames(jsonData []byte) ([]byte, error) {
	var dashboard map[string]any
	if err := json.Unmarshal(jsonData, &dashboard); err != nil {
		return nil, err
	}

	// Navigate directly to spec.panels
	if spec, ok := dashboard["spec"].(map[string]any); ok {
		if panels, ok := spec["panels"].(map[string]any); ok {
			for _, panel := range panels {
				if panelMap, ok := panel.(map[string]any); ok {
					if panelSpec, ok := panelMap["spec"].(map[string]any); ok {
						if queries, ok := panelSpec["queries"].([]any); ok {
							for _, query := range queries {
								if queryMap, ok := query.(map[string]any); ok {
									if querySpec, ok := queryMap["spec"].(map[string]any); ok {
										if plugin, ok := querySpec["plugin"].(map[string]any); ok {
											if pluginSpec, ok := plugin["spec"].(map[string]any); ok {
												if datasource, ok := pluginSpec["datasource"].(map[string]any); ok {
													if kind, hasKind := datasource["kind"]; hasKind {
														pluginSpec["datasource"] = map[string]any{"kind": kind}
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return json.MarshalIndent(dashboard, "", "  ")
}
