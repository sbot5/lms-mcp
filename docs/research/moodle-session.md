# Moodle browser-session access notes

Research snapshot from 2026-10. It covers Moodle `MOODLE_405_STABLE` (4.5.14+) and `MOODLE_501_STABLE` (5.1.7+, which keeps its code under `public/`); both branches agree unless noted. No live requests were made.

Confidence tags:
- **V**: verified in source. Source prefixes: M = github.com/moodle/moodle, S = catalyst/moodle-auth_saml2, C = chromedp/chromedp, Cr = chromium/chromium.
- **L**: likely.
- **I**: inferred.

Context: the target site has web services on but the mobile service off, so students get no web-service token. lms-mcp therefore uses a browser session: the `MoodleSession…` cookie plus `sesskey`.

## 1. AJAX endpoint (V M `lib/ajax/service.php`, `lib/external/classes/external_api.php`, `lib/amd/src/ajax.js`)

- **Request:** `POST /lib/ajax/service.php?sesskey=K` with the session cookie and `Content-Type: application/json`. The body is `[{"index":0,"methodname":"…","args":{…}}, …]`.
  - `info=` is ignored, and GET args are not read.
  - Adding `&nosessionupdate=true` stops the call from extending the session.
- **Reply:** an array with one entry per call, `{"error":false,"data":…}` or `{"error":true,"exception":{"message","errorcode","link","moreinfourl"}}`. The first error stops the batch, so the remaining entries are missing.
- **Request-level failures** (bad JSON, unknown function, IP mismatch) return a single object `{"error":"msg","errorcode":…}`.
- **Error codes:** `servicerequireslogin` (session dead or expired), `invalidsesskey`, `servicenotavailable` (not AJAX-enabled), `requireloginerror` (no access to that course).
- **sesskey:** 10 characters, fixed for the life of the session. Every page's `M.cfg` holds `sesskey`, `sessiontimeout`, `userId`, `contextid` and `courseContextId`. The cheapest page to read it from is `/user/preferences.php`, which logs no event (`/my/` logs `dashboard_viewed`). Under CDP, evaluate `M.cfg.sesskey`.
- **Side effects:** AJAX calls log no events (`webservice_function_called` only fires in `webservice/lib.php`) and do not update last access (`lib/moodlelib.php`).

## 2. Which functions are AJAX-enabled (V `db/services.php`, identical in 4.5 and 5.1; all require login)

| Function | AJAX | Notes |
|---|---|---|
| core_course_get_enrolled_courses_by_timeline_classification | yes | |
| core_courseformat_get_state | yes | `data` is a JSON string. Each cm has name, view URL, module, visibility and completion. Each section has a title and a cmlist. No files, descriptions or context ids. |
| core_course_get_updates_since, core_course_check_updates | yes | Per cm: configuration, contentfiles (ids), completion and gradeitems; assign/quiz grades; forum discussion ids; book chapter ids. `since=0` lists everything. |
| core_calendar_get_action_events_by_timesort | yes | limit 1–50, page with `aftereventid` |
| core_calendar_get_calendar_upcoming_view | yes | capped at 21 days or 10 events. Month and day views are AJAX too. |
| core_get_fragment (component `core_courseformat`, callbacks `section` / `cmitem`) | yes | returns course-page HTML (`course/format/lib.php`) |
| core_courseformat_get_overview_information | 5.1+ only | own grade, due date, completion, submission status |
| mod_forum_get_discussion_posts | yes | 4.5: never marks read. 5.0+: marks read when the user tracks the forum (off by default). Accepted under D7. |
| message_popup_get_popup_notifications, core_message_get_conversations, core_message_get_conversation_messages | yes | do not mark read |
| core_session_time_remaining, core_session_touch | yes | |
| core_course_get_contents, core_course_get_course_module, core_calendar_get_calendar_events, gradereport_user_* / gradereport_overview_*, mod_assign_get_assignments / get_submission_status, mod_quiz_* getters, mod_forum_get_forums_by_courses / get_forum_discussions, mod_page / book / folder / resource / url getters | **no** | return `servicenotavailable`. `core_grades_get_feedback` is AJAX-enabled but teacher-only. |

## 3. HTML and file routes (V M; "VC" = view-based activity completion)

| Route | What it logs |
|---|---|
| course/view.php | course_viewed |
| mod/resource/view.php, including `redirect=1` (logged before the 303) | course_module_viewed + VC |
| pluginfile.php (checked handlers) | nothing |
| mod/folder/view.php (an inline folder 303s to the course) | course_module_viewed + VC |
| mod/folder/download_folder.php | all_files_downloaded + VC |
| mod/page, mod/url, mod/quiz view.php | course_module_viewed + VC |
| mod/book/view.php | course_module_viewed + chapter_viewed per chapter; VC on the last chapter |
| mod/book/tool/print/index.php | book_printed |
| mod/assign/view.php | course_module_viewed, submission_status_viewed, feedback_viewed + VC |
| grade/report/user, grade/report/overview | grade_report_viewed |
| course/resources.php | course_resources_list_viewed (links only to view.php) |
| course/downloadcontent.php (needs `downloadcoursecontentallowed`, off by default) | nothing |
| user/preferences.php, calendar/export.php?course=ID | nothing (good for reading `M.cfg`) |

Getting direct file URLs:
- **Resource:** the course page links only to view.php, and cm customdata holds only size, type and date. Send `mod/resource/view.php?id=N&redirect=1` once **without following the redirect**. Its `Location` is `/pluginfile.php/{ctx}/mod_resource/content/{rev}/{file}`, which can be reused (`rev` is ignored). Repeat only if `contentfiles` changes in `get_updates_since`.
- **Inline folder:** its pluginfile links are in the `section` fragment (`mod/folder/renderer.php`).
- **Page:** `/pluginfile.php/{ctx}/mod_page/content/index.html` logs nothing.
- **Book chapter:** `/pluginfile.php/{ctx}/mod_book/chapter/{chapterid}/index.html` logs nothing. Chapter ids come from `get_updates_since`.
- **Module context id:** no AJAX call exposes it. Learn it once from `M.cfg.contextid` on one view (or the book print page), then store it.
- **Course zip** (if enabled): covers resources, folders and pages without logging.
- **Assignment feedback files and annotated PDFs:** only on assign/view.php.

## 4. Grades

- No AJAX call returns a student's grades on 4.5/5.0. On 5.1, `core_courseformat_get_overview_information` covers activity grades only.
- **User report:** one event per course. Its feedback column (on by default) shows assignment comments, which are pushed to the gradebook by default.
- **Overview report:** totals for all courses in one event.
- **Change detection:** the `gradeitems` entries in `get_updates_since` (dategraded > since) show which cms got grades. Open the user report only for those courses.
- **Notifications:** popups for assignment `feedbackavailable` (customdata `cmid`) and quiz `attempt_grading_complete`. They are sent only when the teacher chooses to notify, and gradebook-only edits send nothing, so treat them as triggers, not as the source of truth.

## 5. Session facts (V M `lib/classes/session/manager.php`)

- **Timeout:** `sessiontimeout` defaults to 8 h; `M.cfg.sessiontimeout` has the real value.
- **Sliding window:** every request extends the session (written at most every 20 s), except requests with `nosessionupdate`. Hourly sync therefore keeps a session alive while the PC is on.
- **IP binding:** `tracksessionip` is off by default and can only be turned on in config.php. If it is on, a request from a new IP destroys the session, the browser's too (`sessionipnomatch2`). On 2026-10-02 the user's browser stayed logged in after a cloud request, but that request probably carried no cookie, so this is not yet proven.
- **No user-agent binding,** and auth_saml2 does not re-check the IdP on each request (S `db/hooks.php`). A copied cookie should therefore work from elsewhere (L).
- **What kills a session:** logout (auth_saml2 also clears its SP cookie and may do IdP single logout), logging in again in the same browser, or Preferences → Browser sessions. `limitconcurrentlogins` defaults to 0 (unlimited).
- **Cookie:** the name is `MoodleSession` + `$CFG->sessioncookie`. It is a browser-session cookie, HttpOnly, SameSite=Lax.
- **Expiry signals:**
  - AJAX returns `servicerequireslogin`.
  - Pages and pluginfile return a 3xx, either straight to the IdP (when auth_saml2 dual login is off or passive) or `303 /login/index.php`.
  - Treat any 3xx to `/login/`, `/auth/saml2/` or another host as expired.

## 6. Calendar export (V M `calendar/export_execute.php`, `calendar/lib.php`)

- **URL:** `/calendar/export_execute.php?userid=U&authtoken=T&preset_what=all|courses|groups|user|categories&preset_time=weeknow|weeknext|monthnow|monthnext|recentupcoming|custom`. No cookie is needed.
- **Token:** `T = sha1(userid . password-field . calendar_exportsalt)`. SAML users store "not cached" in the password field, so T stays stable until an admin changes the salt or the user's auth method. Get the URL once from `calendar/export.php` ("Get calendar URL"; the site's theme hides the link to this page). Keep it secret.
- **Events:** everything the user's calendar shows, including activity due/open/close dates, filtered by cm visibility; active enrolments only.
- **Ranges:** `recentupcoming` covers −5 to +60 days; `custom` covers −5 to +365 days by default. Events are selected by start time, with no paging.
- **Errors:** `no export` means export is disabled; `Invalid authentication` means a bad token.

## 7. Driving a dedicated Edge profile with chromedp (Windows)

- **Launch:**
  - Do not use `DefaultExecAllocatorOptions`: it adds headless, enable-automation and disable-extensions (C `allocate.go`).
  - Set `ExecPath` to msedge.exe; chromedp only searches for Chrome.
  - Use a dedicated `UserDataDir`. Chromium ≥136 refuses remote debugging on the default profile (Cr; Edge L).
- **Avoid headless:** `--headless` (the new headless since M132) adds `HeadlessChrome` to the user agent, sets `navigator.webdriver`, and matches a common EDR detection rule. Prefer a headed window minimised with `Browser.setWindowBounds`.
- **Silent re-auth:**
  - Open the auth_saml2 login URL from the public config (`/auth/saml2/login.php?wants=…&idp=…&passive=on`). On failure it lands on `/login/index.php?saml=0` (S `auth.php`).
  - It works only while the IdP session lives and its policy does not re-prompt (L).
  - IdP cookies usually die with the browser unless the organisation enables persistent sessions and the user ticks "Stay signed in" (L).
- **Cookies:** `network.GetCookies().WithURLs(...)` and `storage.GetCookies()` return HttpOnly cookies (V cdproto).
- **Profile lock:**
  - A second launch on a profile already in use exits, and chromedp reports "chrome failed to start" or a websocket timeout. The user's normal Edge profile does not conflict (L).
  - To attach to a running instance, use its `DevToolsActivePort` with `NewRemoteAllocator`.
  - Close with `chromedp.Cancel`; a hard kill marks the profile as crashed (L).
  - The `RemoteDebuggingAllowed=false` policy blocks CDP entirely.
- **Detecting the outcome:** landing on wwwroot outside `/login` and `/auth/saml2` means success. If the IdP sign-in widget appears, or nothing progresses for 20 s, show the window and let the user log in.

## 8. Prior art

- ventz/aws-login (Go): SAML login via chromedp with a persisted profile. It tries a 20-second login with warm cookies first, then falls back to a visible window.
- alexFiorenza/aulasvirtuales-toolkit: Playwright login, then AJAX for courses, course state, events and forum posts. Its ADR-004 notes that grades, assignments and file URLs still needed HTML.
- alvaro-salort/moodle-scraper: its AJAX `core_course_get_contents` call fails, so it falls back to course/view.php.
- kesaruhasun/mcp-sliit-courseweb: its instructions read the cookie with `document.cookie`, which cannot see HttpOnly cookies.

## Open questions (checks for M1, each one request)

| Question | Check |
|---|---|
| Is the site on 5.1? | AJAX `core_courseformat_get_overview_information {courseid, modname:"assign"}`: data means yes, a top-level `invalidrecord` means no |
| 4.5 or 5.0? | AJAX `mod_quiz_get_user_quiz_attempts`: `servicenotavailable` means ≥5.0, `invalidrecord` means 4.5 |
| Does the cloud session work (no IP binding)? | `GET /user/preferences.php` with the injected cookie returns 200 |
| Dual login or straight to the IdP? | without a cookie, `curl -sI /my/`: Location is `/login/` or the IdP |
| Does the section fragment work for this course format? | one `core_get_fragment` `section` call returns HTML |
| Is course zip enabled? | `GET /course/downloadcontent.php?contextid=C` without following redirects: 200 or 303 |
| Is forum tracking on? | `GET /user/forum.php` |
| Does passive SAML re-auth work, and do IdP cookies persist? | Windows only (M3): dedicated Edge profile with `passive=on`; after a restart, check `storage.GetCookies` for persistent IdP cookies |
