@AGENTS.md

## Claude Code notes

- Keep the main context for architecture, integration and review. Send self-contained work (API research, a single package with a clear spec) to the `researcher` or `implementer` subagents, and ask them for short reports.
- Live checks against the user's Moodle site or Ed run only in cloud sessions whose environment allows those hosts and provides `MOODLE_BASE_URL`, the Ed token (`ED_API_TOKEN` or `ED_AUTH=proxy`), the Moodle session (`MOODLE_COOKIE` or `MOODLE_AUTH=proxy`) and `MOODLE_ICAL_URL`. The repository is public: never commit the school's name, its Moodle hostname or anything else that identifies the user. Ask the user before the first live request of a session. Never send any side-effecting request, even to test something.
