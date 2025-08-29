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
	"regexp"
	"runtime"
	"strings"
	"time"

	persesv1 "github.com/perses/perses/pkg/model/api/v1"
	"github.com/perses/perses/pkg/model/api/v1/common"
)

var (
	inputDir                   = flag.String("input-dir", "", "Absolute path to input directory containing Plutono dashboard JSON files to migrate (required)")
	outputDir                  = flag.String("output-dir", "", "Absolute path to output directory for migrated files (default: <input-dir>/.migrated)")
	cleanUp                    = flag.Bool("cleanup", true, "Cleanup containers after migration (default: false)")
	grafanaPort                = flag.String("grafana-port", "3000", "Port for Grafana container")
	persesPort                 = flag.String("perses-port", "8080", "Port for Perses container")
	waitTime                   = flag.Duration("wait", 10*time.Second, "Time to wait for containers to start (default: 10s)")
	persesVersion              = flag.String("perses-version", "0.52.0-beta.3", "Version of percli to download (default: 0.52.0-beta.3)")
	persesDockerImage          = flag.String("perses-docker-image", "persesdev/perses:latest", "Docker image for Perses container (default: persesdev/perses:latest)")
	recursive                  = flag.Bool("recursive", false, "Process JSON files recursively in subdirectories (default: false)")
	useDefaultPersesDatasource = flag.Bool("use-default-perses-datasource", true, "Remove datasource names to use default Perses datasource (default: true)")
	help                       = flag.Bool("help", false, "Show help message")
)

type DashboardInfo struct {
	UID          string
	RelativePath string // relative path from input directory
}

type MigrationSummary struct {
	TotalDashboards     int
	SchemaUpdateSuccess int
	SchemaUpdateFailed  []string
	ExportSuccess       int
	ExportFailed        []string
	MigrationSuccess    int
	MigrationFailed     []string
}

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

	dashboards, summary, err := updateGrafanaSchemasToLatestVersion(*inputDir, *grafanaPort)
	if err != nil {
		log.Fatalf("Import failed: %v", err)
	}

	fmt.Printf("✓ Schema update completed\n\n")

	grafanaOutputDir := filepath.Join(*outputDir, "grafana-schema-latest")
	if err := exportUpdatedGrafanaDashboards(dashboards, grafanaOutputDir, *grafanaPort, summary); err != nil {
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
	if err := migrateDashboardsToPerses(grafanaOutputDir, persesOutputDir, summary); err != nil {
		log.Printf("Warning: Failed to migrate dashboards to Perses: %v", err)
	}

	// Display migration summary
	displayMigrationSummary(summary)

	fmt.Printf("\n🎉 Migration completed!\n")
	fmt.Printf("📁 Perses dashboards are available at: %s\n", persesOutputDir)
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
	fmt.Printf("Starting Grafana container...\n")
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
		if err := startContainer("perses", *persesDockerImage, port, "8080"); err != nil {
			return fmt.Errorf("failed to start Perses container: %v", err)
		}
		fmt.Printf("Waiting for Perses to start...\n")
		time.Sleep(*waitTime)
	}

	fmt.Printf("Perses container ready on port %s\n", port)
	return nil
}

func collectJSONFiles(inputDir string) ([]string, error) {
	var files []string

	if *recursive {
		// Recursive mode: walk through all subdirectories
		err := filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		// Non-recursive mode: only root directory (existing behavior)
		globFiles, err := filepath.Glob(filepath.Join(inputDir, "*.json"))
		if err != nil {
			return nil, err
		}
		files = globFiles
	}

	return files, nil
}

func updateGrafanaSchemasToLatestVersion(inputDir, port string) ([]DashboardInfo, *MigrationSummary, error) {
	files, err := collectJSONFiles(inputDir)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find JSON files: %v", err)
	}

	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no JSON files found in directory: %s", inputDir)
	}

	fmt.Printf("Updating schemas for %d dashboards...\n", len(files))

	summary := &MigrationSummary{
		TotalDashboards: len(files),
	}

	var dashboards []DashboardInfo
	for _, file := range files {
		relPath, err := filepath.Rel(inputDir, file)
		if err != nil {
			log.Printf("Warning: Failed to get relative path for %s: %v", file, err)
			continue
		}

		fmt.Printf("Processing file: %s\n", file)
		fmt.Printf("Importing: %s\n", filepath.Base(file))
		uid, err := importDashboardToGrafana(file, port)
		if err != nil {
			log.Printf("Warning: Failed to import %s: %v", filepath.Base(file), err)
			summary.SchemaUpdateFailed = append(summary.SchemaUpdateFailed, filepath.Base(file))
			continue
		}

		fmt.Printf("Successfully imported: %s\n", filepath.Base(file))
		summary.SchemaUpdateSuccess++
		dashboards = append(dashboards, DashboardInfo{
			UID:          uid,
			RelativePath: relPath,
		})
	}

	return dashboards, summary, nil
}

func importDashboardToGrafana(inputFile, port string) (string, error) {
	// Import dashboard into Grafana to automatically update its schema to the latest version
	// Grafana normalizes the dashboard format on import, ensuring compatibility with Perses migration
	dashboardData, err := os.ReadFile(inputFile)
	if err != nil {
		return "", fmt.Errorf("failed to read dashboard file: %v", err)
	}

	var dashboard map[string]any
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
	importPayload := map[string]any{
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

	var importResponse map[string]any
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

func exportUpdatedGrafanaDashboards(dashboards []DashboardInfo, outputDir, port string, summary *MigrationSummary) error {
	// Export dashboards from Grafana using collected UIDs from import process
	// This gives us the latest Grafana schema format required for successful Perses migration
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %v", err)
	}

	fmt.Printf("Found %d dashboards to export\n", len(dashboards))

	fmt.Printf("\nExporting dashboards with updated schemas to path %s:\n This is necessary because Perses migration requires the latest Grafana schema format. \n", outputDir)
	exportCount := 0
	for i, dashboard := range dashboards {
		fmt.Printf("  [%d] UID: %s, Path: %s\n", i+1, dashboard.UID, dashboard.RelativePath)
		if err := exportSingleUpdatedDashboard(dashboard.UID, dashboard.RelativePath, outputDir, port); err != nil {
			log.Printf("Warning: Failed to export dashboard %s: %v", dashboard.UID, err)
			summary.ExportFailed = append(summary.ExportFailed, filepath.Base(dashboard.RelativePath))
			continue
		}
		summary.ExportSuccess++
		exportCount++
	}

	fmt.Printf("Successfully exported %d dashboards\n", exportCount)

	return nil
}

func exportSingleUpdatedDashboard(uid, relativePath, outputDir, port string) error {
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

	var dashboardResponse map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&dashboardResponse); err != nil {
		return fmt.Errorf("failed to decode dashboard response: %v", err)
	}

	// Extract spec which contains the dashboard definition
	spec, ok := dashboardResponse["spec"].(map[string]any)
	if !ok {
		return fmt.Errorf("no spec found in dashboard response")
	}

	// Add uid field at root level for Perses dashboard name generation
	// Use the original UID which already follows the correct format
	spec["uid"] = uid

	// Create subdirectory structure based on relative path
	relativeDir := filepath.Dir(relativePath)
	targetDir := outputDir
	if relativeDir != "." {
		targetDir = filepath.Join(outputDir, relativeDir)
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return fmt.Errorf("failed to create subdirectory: %v", err)
		}
	}

	// Use the UID as the base filename
	slug := uid
	if title, ok := spec["title"].(string); ok && title != "" {
		// Clean the title to make it regex-compliant
		slug = sanitizeFilenameForRegex(title)
	}

	timestamp := time.Now().Format("20060102-1504")
	filename := fmt.Sprintf("%s-%s.json", slug, timestamp)

	// Validate that the generated filename matches the required regex pattern
	// Pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$
	if !validateFilenameRegex(filename) {
		log.Printf("Warning: Generated filename '%s' does not match regex pattern, using UID fallback", filename)
		filename = fmt.Sprintf("%s-%s.json", sanitizeFilenameForRegex(uid), timestamp)
	}

	outputPath := filepath.Join(targetDir, filename)

	// Export the spec (dashboard definition) as JSON
	dashboardBytes, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal dashboard: %v", err)
	}

	if err := os.WriteFile(outputPath, dashboardBytes, 0644); err != nil {
		return fmt.Errorf("failed to write dashboard file: %v", err)
	}

	// Show relative path for better user feedback
	displayPath := filename
	if relativeDir != "." {
		displayPath = filepath.Join(relativeDir, filename)
	}
	fmt.Printf("  → Exported dashboard: %s\n", displayPath)
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
	cmd := exec.Command(binPath, "login", loginURL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to login to Perses: %v, output: %s", err, string(output))
	}

	fmt.Printf("✓ Logged into Perses\n")
	return nil
}

func migrateDashboardsToPerses(grafanaOutputDir, persesOutputDir string, summary *MigrationSummary) error {
	binPath := "./bin/percli"

	// Check if percli binary exists
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		return fmt.Errorf("percli binary not found at %s", binPath)
	}

	// Create perses output directory
	if err := os.MkdirAll(persesOutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create perses output directory: %v", err)
	}

	// Find all JSON files in grafana output directory recursively
	var files []string
	err := filepath.Walk(grafanaOutputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to find JSON files in grafana output directory: %v", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no JSON files found in grafana output directory: %s", grafanaOutputDir)
	}

	fmt.Printf("Found %d Grafana dashboards to migrate to Perses\n", len(files))

	// Show datasource handling strategy
	if *useDefaultPersesDatasource {
		fmt.Printf("Datasource strategy: Removing datasource names to use default Perses datasource\n")
	} else {
		fmt.Printf("Datasource strategy: Preserving original datasource names\n")
	}

	fmt.Printf("\nMigrating dashboards to Perses Schema format:\n")

	migratedCount := 0
	for i, file := range files {
		// Get relative path from grafana output directory
		relPath, err := filepath.Rel(grafanaOutputDir, file)
		if err != nil {
			log.Printf("Warning: Failed to get relative path for %s: %v", file, err)
			continue
		}

		fmt.Printf("  [%d/%d] Migrating: %s\n", i+1, len(files), relPath)

		// Construct output file path in perses directory maintaining subdirectory structure
		outputFile := filepath.Join(persesOutputDir, relPath)

		// Create subdirectory if needed
		outputDir := filepath.Dir(outputFile)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			log.Printf("Warning: Failed to create output directory %s: %v", outputDir, err)
			continue
		}

		// Run percli migrate command
		cmd := exec.Command(binPath, "migrate", "--online", "-f", file, "-o", "json")
		output, err := cmd.Output()
		if err != nil {
			log.Printf("Warning: Failed to migrate %s: %v", filepath.Base(file), err)
			summary.MigrationFailed = append(summary.MigrationFailed, filepath.Base(file))
			continue
		}

		// Handle datasource references based on flag
		var cleanedOutput []byte
		if *useDefaultPersesDatasource {
			// Remove datasource names to use default Perses datasource
			var err error
			cleanedOutput, err = removeDatasourceNames(output)
			if err != nil {
				log.Printf("Warning: Failed to clean datasource references in %s: %v", filepath.Base(file), err)
				cleanedOutput = output // Use original output if cleanup fails
			}
		} else {
			// Keep original datasource names
			cleanedOutput = output
		}

		// Save the migrated dashboard to perses output directory
		if err := os.WriteFile(outputFile, cleanedOutput, 0644); err != nil {
			log.Printf("Warning: Failed to save migrated dashboard %s: %v", filepath.Base(file), err)
			summary.MigrationFailed = append(summary.MigrationFailed, filepath.Base(file))
			continue
		}

		fmt.Printf("    → Successfully migrated to: %s\n", relPath)
		summary.MigrationSuccess++
		migratedCount++
	}

	fmt.Printf("Successfully migrated %d/%d dashboards to Perses Schema format\n", migratedCount, len(files))
	return nil
}

func displayMigrationSummary(summary *MigrationSummary) {
	fmt.Printf("\n%s\n", strings.Repeat("=", 60))
	fmt.Printf("                    MIGRATION SUMMARY\n")
	fmt.Printf("%s\n", strings.Repeat("=", 60))

	fmt.Printf("Total Grafana dashboards processed: %d\n\n", summary.TotalDashboards)

	// Schema Update Results
	fmt.Printf("Grafana Schema Update: %d successful, %d failed\n", summary.SchemaUpdateSuccess, len(summary.SchemaUpdateFailed))
	if len(summary.SchemaUpdateFailed) > 0 {
		fmt.Printf("  Failed schema updates:\n")
		for _, name := range summary.SchemaUpdateFailed {
			fmt.Printf("    - %s\n", name)
		}
	}

	// Export Results
	fmt.Printf("\nExport: %d successful, %d failed\n", summary.ExportSuccess, len(summary.ExportFailed))
	if len(summary.ExportFailed) > 0 {
		fmt.Printf("  Failed exports:\n")
		for _, name := range summary.ExportFailed {
			fmt.Printf("    - %s\n", name)
		}
	}

	// Migration Results
	fmt.Printf("\nPerses Migration: %d successful, %d failed\n", summary.MigrationSuccess, len(summary.MigrationFailed))
	if len(summary.MigrationFailed) > 0 {
		fmt.Printf("  Failed migrations:\n")
		for _, name := range summary.MigrationFailed {
			fmt.Printf("    - %s\n", name)
		}
	}

	// Overall Success Rate
	totalFailures := len(summary.SchemaUpdateFailed) + len(summary.ExportFailed) + len(summary.MigrationFailed)
	successRate := float64(summary.TotalDashboards-totalFailures) / float64(summary.TotalDashboards) * 100
	fmt.Printf("\nOverall Success Rate: %.1f%%\n", successRate)

	if totalFailures == 0 {
		fmt.Printf("✓ All dashboards migrated successfully!\n")
	} else {
		fmt.Printf("⚠ %d dashboard(s) encountered issues during migration\n", totalFailures)
	}
	fmt.Printf("%s\n", strings.Repeat("=", 60))
}

func removeDatasourceNames(jsonData []byte) ([]byte, error) {
	var dashboard persesv1.Dashboard
	if err := json.Unmarshal(jsonData, &dashboard); err != nil {
		return nil, err
	}

	// Iterate through panels and clean datasource references

	for _, panel := range dashboard.Spec.Panels {
		for _, query := range panel.Spec.Queries {
			cleanDatasourceInPlugin(&query.Spec.Plugin)
		}
	}

	return json.MarshalIndent(dashboard, "", "  ")
}

func cleanDatasourceInPlugin(plugin *common.Plugin) {
	// Only clean datasource references if the flag is set to use default Perses datasource
	if !*useDefaultPersesDatasource {
		return
	}

	// Access the datasource from the plugin spec
	if pluginSpec, ok := plugin.Spec.(map[string]any); ok {
		if datasourceRef, ok := pluginSpec["datasource"].(map[string]any); ok {
			// Remove the "default" property
			delete(datasourceRef, "default")

			// Remove the "name" property
			delete(datasourceRef, "name")

			// Remove the "spec" property from plugin if it exists
			if pluginData, hasPlugin := datasourceRef["plugin"].(map[string]any); hasPlugin {
				delete(pluginData, "spec")
			}
		}
	}
}

// sanitizeFilenameForRegex converts a dashboard title to a filename that complies with
// the regex pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$
func sanitizeFilenameForRegex(title string) string {
	// Convert to lowercase
	result := strings.ToLower(title)

	// Replace any non-alphanumeric characters with hyphens using regex
	reg := regexp.MustCompile(`[^a-z0-9]+`)
	result = reg.ReplaceAllString(result, "-")

	// Remove leading and trailing hyphens
	result = strings.Trim(result, "-")

	// Ensure the result is not empty
	if result == "" {
		return "dashboard"
	}

	return result
}

// validateFilenameRegex validates that a filename matches the required regex pattern
func validateFilenameRegex(filename string) bool {
	// Remove .json extension for validation
	name := strings.TrimSuffix(filename, ".json")

	// Regex pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$
	// This matches the main part of the full pattern for the basename
	pattern := `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	matched, err := regexp.MatchString(pattern, name)
	if err != nil {
		return false
	}
	return matched
}
