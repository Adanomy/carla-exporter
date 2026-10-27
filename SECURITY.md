# Security

Report vulnerabilities privately through [GitHub private vulnerability reporting](https://github.com/Adanomy/carla-exporter/security/advisories/new).
Include the affected version, impact, reproduction steps, and a minimal example.
Keep credentials and private simulation data out of reports.

Please keep vulnerability details out of public issues and pull requests until
maintainers have coordinated a fix and disclosure with you.

This is a community project; response and remediation times are not guaranteed.

## Deployment boundary

The exporter reads CARLA over RPC and a TCP world stream. It never advances the
simulation or changes world settings. Its HTTP endpoint has no authentication
or TLS; bind it to a trusted network or place it behind an authenticated proxy.
Metrics can include vehicle roles, models, and coordinates.
