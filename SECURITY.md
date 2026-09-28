# Security and private data

Keep your API token, configuration, exports, logs and state outside the source repository. Never attach them to an issue or pull request. Use synthetic fixtures when reporting a bug. Revoke an exposed Ed token through Ed and replace the local token file.

The default configuration omits account identity from `whoami` / `ed_whoami` and excludes private discussion posts and replies. Mention tags are redacted. This is not general text anonymization: free-form post bodies, lesson content, links, and course names may contain personal or confidential information. Exports are private data.

Turning private-post export off hides it from current discussion files and update views after a successful sync. Existing checkpoints, backups, and files from disabled sources can retain old data. Changing a privacy setting does not securely erase historical files. Choose a fresh output directory when you need a new archive with a different privacy policy.

API requests use the configured Ed token only with `https://edstem.org/api`; attachment downloads use a separate unauthenticated client restricted to Ed content hosts. No browser cookies are collected. Downloaded files are not executed.

There is no public HTTP server or GUI listener. MCP uses stdio. Review which MCP clients you trust: `sync_now` writes to the configured output folders and `get_thread` retrieves data from enrolled courses.

For vulnerabilities, use this repository's private vulnerability reporting feature when available. Do not publish credentials or course material in public issues.
