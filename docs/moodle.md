# Moodle integration (development preview)

This branch implements read-only Moodle material synchronization and per-course iCalendar exports. It has been tested against local synthetic HTTP fixtures, **not a live institution's Moodle**. Keep the existing production Ed instance unchanged until a real Moodle course has been validated. The published v0.3.0 binary does not include this preview.

## Choose authentication

- **Token:** set `auth` to `token`. Your site must enable a web service exposing `core_course_get_contents` and allow file downloads for its token. Mobile-app services are not enabled at every institution. The client posts the token to the REST endpoint and adds it only when requesting the site's web-service file endpoint.
- **Cookie:** set `auth` to `cookie`. Save the `Cookie` request-header value from your own authenticated browser session. This is a session credential, not your account password, and it expires. There is no automatic SSO login, MFA bypass, browser-cookie extraction, or session renewal.

The token file-download endpoint and browser-session endpoint are different; see [Moodle's file API documentation](https://moodledev.io/docs/5.0/apis/subsystems/external/files). Both modes make read-only requests; no quiz attempts, uploads, form submissions, or posts are sent.

## Configure privately

Copy `moodle.example.json` and `moodle.env.example` to a private folder outside the code repository. Rename the latter to `moodle.env`. Replace the example site, course ID and credentials. The course ID comes from the Moodle course URL (`course/view.php?id=...`). Keep credentials and token-bearing calendar URLs in the env file; JSON contains only its path and variable names.

Set `moodle.base_url` to the Moodle installation URL, optionally including a subdirectory. HTTPS is required except for loopback test servers. Use **separate, non-overlapping output folders** for Ed and Moodle, such as `courses/COURSE101/ed` and `courses/COURSE101/moodle`. This prevents one provider from owning the other's checkpoint and files.

```sh
go build -trimpath -buildvcs=false -o lms-mcp .
lms-mcp config validate -config /private/moodle.json
lms-mcp sync -config /private/moodle.json
lms-mcp status -config /private/moodle.json
lms-mcp whats-new -config /private/moodle.json -course moodle:COURSE101
```

For Windows, build `lms-mcp.exe` and use `.\lms-mcp.exe`. `courses: []` is valid when `moodle.courses` is configured; a Moodle-only setup does not require an Ed token. Add the `moodle` object to an existing Ed config for a combined instance. Failures are reported per course and do not suppress healthy courses from the other provider.

The Ed `init` wizard remains Ed-only. Set up Moodle with the example JSON in this preview. The same daily Windows task installer accepts Moodle-only configurations, but **do not enable it until the manual live checks below pass**. Neither this preview nor its tests change existing tasks.

## Optional calendar

Use Moodle's calendar export page to get a feed URL for the intended course and date range. Save the complete URL in the private env variable named by `calendar_url_env`. The export URL itself grants access to the feed, so do not paste it into an issue, chat, or public config. [Moodle calendar export documentation](https://docs.moodle.org/31/en/Calendar_export) explains the export-URL flow.

Remove `calendar_url_env` if you only want materials. When configured, the feed must be on the same site and use `/calendar/export_execute.php`. It is requested without the saved browser Cookie or API token; its own URL authenticates access. Select the course and time range carefully: the client exports the configured feed, not an inferred subset of it.

Output includes `_calendar.json` and `_calendar.md`. Event UID, summary, raw DTSTART/DTEND, DTSTART TZID, all-day status, cancellation status and recurrence metadata are retained. Times are not converted, recurring rules are not expanded, and not all events are assignment deadlines. Known credential query parameters are removed from event links. The `get_moodle_calendar` MCP tool reads this successful local snapshot and returns its freshness without contacting Moodle.

## Materials, updates and recovery

- Token mode reads file entries from `core_course_get_contents`. It reuses matching local files when remote modification metadata is unchanged. `-full` downloads again to verify contents.
- Cookie mode follows recognized course sections and resource/folder/page/assignment **view** links. It finds same-site course attachments and resource download redirects. It excludes forms, submissions, quizzes, logout routes and external hosts. Theme-specific or JavaScript-only content may not be discoverable; compare against the browser before trusting coverage.
- Conditional downloads use ETag or Last-Modified when available. If the site provides no usable cache metadata, a response body must be downloaded again to detect changes; unchanged content is not rewritten. The client makes at most about ten sequential requests per second and retries HTTP 429/5xx errors within a bounded window.
- `_index.md` lists files under `files/`; `_updates.md` records material and calendar changes. Moodle uses `moodle:COURSE_CODE` in `whats-new` and status. First import is a baseline rather than new alerts.
- A changed or unavailable file never deletes the old local copy. Checkpoints, backups, local-edit conflict protection and interrupted-write recovery use the same storage engine as Ed.
- Credentials are confined to the configured HTTPS site. Cross-origin redirects and POST redirects are rejected. Login pages and Moodle service errors are rejected rather than saved as materials. An empty response after an existing nonempty material archive also fails visibly for manual access/theme verification.
- A failed file or calendar fetch prevents the entire course checkpoint from advancing. A file is limited to 64 MiB and a course's staged material bodies to 512 MiB; larger exports require a future streaming implementation or manual download.

Cookie expiry is expected: sign in yourself, replace only the Cookie in the private env file, and retry. Raw request URLs, response bodies and credentials are excluded from error messages. Exports can still contain ordinary personal or confidential course content; they are private data, not anonymized public artifacts.

## Live acceptance checklist (not yet performed)

1. Choose one course and a fresh test output folder; keep production exports untouched.
2. Compare the downloaded file inventory to the logged-in browser, including sections and folders.
3. Rerun without changes: expect zero change events; cacheable files should return 304 or skip download.
4. Verify a known changed file and a calendar event's local time against Moodle.
5. Test with an expired session: expect an explicit error, unchanged prior files and unchanged successful checkpoint.
6. Only after those checks, decide whether to enable Moodle in the personal daily task.
