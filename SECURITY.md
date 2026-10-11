# Security policy

Report suspected vulnerabilities privately through
[GitHub's vulnerability reporting page](https://github.com/x-chunk/vela/security/advisories/new).
If private reporting is unavailable, open an issue asking for a private contact
without publishing exploit details, credentials, or signed URLs.

Include the affected version, deployment mode, impact, and a minimal
reproduction using synthetic data. Never send production API keys or storage
credentials. Security fixes target the latest release; update older deployments
before checking whether a report still applies.

API keys belong only in Authorization headers. Signed URLs are bearer
credentials and must not appear in logs. Keep storage buckets private and
configure trusted proxy addresses explicitly. Reverse proxy examples are
starting points: operators own TLS, firewall rules, backups, and updates.
