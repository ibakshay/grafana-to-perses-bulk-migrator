# Grafana to Perses Bulk Migration CLI

A fully automated command-line tool for bulk migration of Grafana dashboards to Perses format. This tool updates Grafana dashboards to the latest Grafana schema version and migrates them to Perses format. For more details about Perses migration, see the [official documentation](https://perses.dev/perses/docs/migration).

## Features

- **Fully Automated Migration**: Complete migration process with no manual intervention required during execution
- **Schema Updates**: Automatically updates Grafana dashboard schemas to the latest version
- **Recursive Processing**: Option to process dashboards in subdirectories
- **Container Management**: Automatically starts and manages Grafana and Perses containers
- **Detailed Reporting**: Provides comprehensive migration summary with success/failure statistics
- **Cleanup**: Automatic container cleanup after migration (configurable)
- **Cross-Platform**: Supports Linux, macOS, and Windows
1
## Prerequisites

- **Docker**: Required for running Grafana and Perses containers
- **Go 1.24.0+**: For building the tool from source
- **Internet Access**: For downloading percli binary and Docker images

## Installation

### Download from Releases (Recommended)

1. Download the latest binary from the Releases. 
2. Make the binary executable (Linux/macOS only):
   ```bash
   chmod +x ./perses-migration
   ```

### Build from Source (Alternative)

1. Clone or navigate to the migration directory
2. Build the binary:
   ```bash
   make build # will store the binary in ./bin
   ```

3. (Optional) Install to system PATH:
   ```bash
   make install
   ```

## Usage

### Command Line Interface

```bash
./perses-migration [flags]
```

### Available Flags

| Flag | Description | Default | Required |
|------|-------------|---------|----------|
| `--input-dir` | Absolute path to directory containing Grafana dashboard JSON files | - | ✅ |
| `--output-dir` | Absolute path to output directory for migrated files | `<input-dir>/.migrated` | ❌ |
| `--cleanup` | Cleanup containers after migration | `true` | ❌ |
| `--grafana-port` | Port for Grafana container | `3000` | ❌ |
| `--perses-port` | Port for Perses container | `8080` | ❌ |
| `--wait` | Time to wait for containers to start | `10s` | ❌ |
| `--perses-version` | Version of percli to download | `0.52.0-beta.3` | ❌ |
| `--recursive` | Process JSON files recursively in subdirectories | `false` | ❌ |
| `--use-default-perses-datasource` | Remove datasource names to use default Perses datasource | `true` | ❌ |
| `--help` | Show help message | `false` | ❌ |

## Migration Process

The tool performs the following steps automatically:

1. **Container Setup**: Starts Grafana and Perses containers
2. **Schema Update**: Imports dashboards to Grafana to update schemas to latest version
3. **Export**: Exports updated dashboards from Grafana
4. **Tool Setup**: Downloads and configures percli (Perses CLI)
5. **Migration**: Converts Grafana dashboards to Perses format
6. **Cleanup**: Removes containers (if enabled)
7. **Summary**: Displays detailed migration results

## Output Structure

```
<output-dir>/
├── grafana-schema-latest/     # Updated Grafana dashboards
│   └── [dashboard files with timestamp]
└── perses/                    # Migrated Perses dashboards
    └── [dashboard files in Perses format]
```

## Examples

### Basic Migration
```bash
./perses-migration --input-dir=/path/to/dashboards
```

### Recursive Migration with Custom Output
```bash
./perses-migration --input-dir=/path/to/dashboards --output-dir=/path/to/output --recursive
```

### Migration with Custom Ports and Wait Time
```bash
./perses-migration --input-dir=/path/to/dashboards --grafana-port=3001 --perses-port=8081 --wait=30s
```


## Post-Migration Manual Steps

⚠️ **Important**: While the migration process is fully automated, **manual verification and adjustment of dashboards in Perses is required** after migration.

### What to Check:

1. **Panel Types**: Some Grafana panel types may not have direct Perses equivalents
2. **Data Sources**: Verify data source configurations are correct
3. **Queries**: Review and test all dashboard queries
4. **Visualizations**: Check that charts and graphs display correctly
5. **Variables**: Ensure dashboard variables work as expected
6. **Layouts**: Verify panel positioning and sizing

### Recommended Workflow:

1. Run the migration tool
2. Import the generated Perses dashboards into your Perses instance via CLI or UI
3. Review each dashboard
4. Manually adjust unsupported panels or configurations
5. Test all functionality before deploying to production


## Troubleshooting

### Common Issues

**Docker containers fail to start**
- Check if ports are already in use
- Ensure Docker daemon is running
- Try different ports using `--grafana-port` and `--perses-port`

**Permission denied errors**
- Ensure input/output directories have proper permissions
- Run with appropriate user permissions

**Migration failures**
- Check dashboard JSON syntax
- Ensure dashboards are valid Grafana format
- Review migration summary for specific error details

**percli download fails**
- Check network connectivity
- Verify the specified Perses version exists
- Check GitHub API rate limits

## Migration Summary

The tool provides a detailed summary showing:

- Total dashboards processed
- Schema update results (success/failed)
- Export results (success/failed) 
- Perses migration results (success/failed)
- Overall success rate
- List of failed items for troubleshooting

### Example Output

```
============================================================
                    MIGRATION SUMMARY
============================================================
Total Grafana dashboards processed: 500

Grafana Schema Update: 500 successful, 0 failed

Export: 498 successful, 2 failed
  Failed exports:
    - corrupted-dashboard.json
    - invalid-json-format.json

Perses Migration: 496 successful, 2 failed
  Failed migrations:
    - dashboard-with-unsupported-panel.json
    - complex-templating.json

Overall Success Rate: 99.2%
⚠ 4 dashboard(s) encountered issues during migration
============================================================
```


## License

The code is licensed under an [Apache 2.0](./LICENSE) license.

