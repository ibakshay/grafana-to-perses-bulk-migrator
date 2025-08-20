# Grafana schema migration 


Start a Grafana container
1. `docker run -p 3000:3000 grafana/grafana:11.0.0`

2. Run the following Curl to migrate the schema of the grafana dashboards to the latest version

```bash
curl -X POST http://admin:admin@localhost:3000/api/dashboards/import \                                                                                                                                                                                               
-H 'Accept: application/json' \
-H 'Content-Type: application/json' \
-d @< path to file>
```
