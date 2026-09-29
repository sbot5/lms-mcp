# Security and private data

Keep your API token, configuration, exports, logs and state outside the source repository. Never attach them to an issue or pull request. Use synthetic fixtures when reporting a bug. Revoke an exposed Ed token through Ed and replace the local token file.

The default configuration omits account identity from `whoami` / `ed_whoami` and excludes private discussion posts and replies. Mention tags are redacted. This is not general text anonymization: free-form post bodies, lesson content, links, and course names may contain personal or confidential information. Exports are private data.

Turning private-post export off hides it from current discussion files and update views after a successful sync. Existing checkpoints, backups, and files from disabled sources can retain old data. Changing a privacy setting does not securely erase historical files. Choose a fresh output directory when you need a new archive with a different privacy policy.

API requests use the configured Ed token only with `https://edstem.org/api`; attachment downloads use a separate unauthenticated client restricted to Ed content hosts. No browser cookies are collected. Downloaded files are not executed.

There is no public HTTP server or GUI listener. MCP uses stdio. Review which MCP clients you trust: `sync_now` writes to the configured output folders and `get_thread` retrieves data from enrolled courses.

The Moodle development preview accepts a user-supplied session Cookie or web-service token from a private env file. It never extracts browser sessions or automates SSO/MFA. Its optional iCalendar export URL is also a credential and belongs only in that env file. Moodle requests are restricted to the configured site, cross-origin/POST redirects are refused, and response bodies/URLs are redacted from errors. Cookie-mode discovery is theme-dependent and requires live validation before production use. No real institution session was used in offline tests.

For vulnerabilities, use this repository's private vulnerability reporting feature when available. Do not publish credentials or course material in public issues.
