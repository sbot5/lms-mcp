# Moodle Web Services notes (student view)

Research snapshot from 2026-10. Sources: Moodle `main` (5.3dev, older branches diffed), the Moodle app, and moodle-dl. Nothing here was called against a live site.

Confidence: **V** verified in source, **L** likely, **I** inferred.

Source prefixes:
- `M:` = github.com/moodle/moodle/blob/main/public/
- `A:` = github.com/moodlehq/moodleapp/blob/main/src/
- `D:` = github.com/C0D3D3V/Moodle-DL/blob/main/moodle_dl/

## 1. Getting a token

### a. Password: `POST /login/token.php` (V M:login/token.php)

Send `username, password, service=moodle_mobile_app` and get back `{"token","privatetoken"}`. GET is refused.

- Works only for manual/LDAP accounts. SSO plugins (OAuth2, Shibboleth, SAML) cannot check a password, so they return `invalidlogin`.
- IdP MFA never runs.
- Failures count toward account lockout.
- **Not usable on an SSO + MFA site.**

### b. SSO launch: the Moodle app's flow (V M:admin/tool/mobile/launch.php, A:core/features/login/services/login-helper.ts)

- **Flow:** open `{wwwroot}/admin/tool/mobile/launch.php?service=moodle_mobile_app&passport=P&urlscheme=moodlemobile[&oauthsso=ID][&confirmed=1]` in the browser. The user goes through login, the IdP and MFA, then gets a `302 Location: moodlemobile://token=B64`.
- **With `confirmed=1`** the page renders a "click here to launch the app" link whose `href` is the `moodlemobile://token=…` URL. The user can right-click and copy it, which is easier than digging in DevTools.
- **Payload:** `base64(md5(wwwroot . P) ":::" token [":::" privatetoken])`.
  - Verify part 0 against `md5(wwwroot . P)`, using `wwwroot` from the public config and the exact passport string. The app also retries with http/https swapped.
- **privatetoken:** sent only over https, only to non-admins, and only if the user just logged in or the token is new.
- **Blockers:**

  | Cause | Error |
  |---|---|
  | web services off | `enablewsdescription` |
  | mobile service off | `servicenotavailable` |
  | `createmobiletoken` capability removed | `cannotcreatetoken` |
  | `typeoflogin=1`, OAuth2 disabled and no fresh login | `pluginnotenabledorconfigured` |

  For the last one on ≥4.3: launch.php sets a 15-minute `tool_mobile_launch` cookie, and the next web login returns to it. So log out, open launch.php, then log in. Versions ≤4.2 have no workaround.
- **`forcedurlscheme`** (default `moodlemobile`) overrides `urlscheme`.
- **Capture options:**
  - copy the link from the `confirmed=1` page;
  - DevTools → Network → the failed `moodlemobile://` request (the moodle-dl CLI way, D:cli/moodle_wizard.py);
  - a user-level OS handler for `moodlemobile://`. On Windows this is an HKCU registry key; it may clash with the official desktop app (I).

### c. QR code and the "Security keys" page: dead ends

- QR login needs `qrcodetype=2` and a `MoodleMobile` user agent.
- `user/managetoken.php` needs `webservice:createtoken`, which only managers have by default, and since 4.3 it shows token names, not values.

### d. Unauthenticated probe (V M:lib/ajax/service.php, M:admin/tool/mobile/classes/api.php)

`POST {site}/lib/ajax/service-nologin.php?info=tool_mobile_get_public_config` with body `[{"index":0,"methodname":"tool_mobile_get_public_config","args":{}}]` returns `[{"error":false,"data":{…}}]`. It works even with web services off.

Fields:
- `wwwroot`
- `typeoflogin`: 1 app, 2 browser, 3 embedded browser
- `launchurl`
- `identityproviders[{name,url}]`: the URL path reveals oauth2/shibboleth/saml2/oidc
- `enablewebservices`, `enablemobilewebservice`, `maintenanceenabled`

There is no version field. Hints: `tool_mobile_qrcodetype` means ≥4.0, `showloginform` ≥4.5, `tool_mfa_enabled` ≥5.2.

### e. Lifetime (V M:lib/external/classes/util.php, M:webservice/lib.php)

- **Expiry:** `validuntil = created + tokenduration`, 12 weeks by default. Students cannot see it (I).
- **One token per user and service:** a valid token is reused, so it is **shared with the official Moodle app**.
- **After expiry:** the first call returns `accessexception` and deletes the token; later calls return `invalidtoken`.
- **Revoked by:** the profile's "Mobile app → Log out", a password change (by default), or admins.
- **privatetoken:** feeds `tool_mobile_get_autologin_key`, which returns a 60-second, IP-bound key; `autologin.php` turns that into a browser session. It requires a `MoodleMobile` user agent, i.e. app impersonation, so lms-mcp does **not** use it.

## 2. Functions (all in `moodle_mobile_app`; V via lib/db/services.php and mod/*/db/services.php)

| Function | Params → key fields | Min |
|---|---|---|
| core_webservice_get_site_info | → userid, release, version, `functions[]`, userprivateaccesskey | ≤3.0 |
| core_enrol_get_users_courses | userid → id, fullname, visible, dates, lastaccess, progress | ≤3.0 |
| core_course_get_enrolled_courses_by_timeline_classification | classification (inprogress…), limit, offset | 3.6 |
| core_course_get_contents | options excludemodules / excludecontents / includestealthmodules / sectionid / cmid / modname → sections (component=subsection 4.5+), modules (uservisible, availabilityinfo, dates, completiondata, description, `contents[]` for resource/folder/page/url/book/imscp) | ≤3.0 |
| mod_{resource,folder,page,url,label,book}_get_*_by_courses | courseids → intro, introfiles, contentfiles, revision | 3.3 |
| mod_assign_get_assignments | → duedate, cutoffdate, grade, introattachments | ≤3.0 |
| mod_assign_get_submission_status | assignid → lastattempt (files, gradingstatus), feedback (grade, comments, files; only once released) | 3.1 |
| mod_quiz_get_quizzes_by_courses | → timeopen/close, timelimit, attempts, review options | 3.1 |
| mod_quiz_get_user_quiz_attempts (≥5.0) / mod_quiz_get_user_attempts (<5.0, deprecated) | quizid | 5.0 / 3.1 |
| mod_quiz_get_user_best_grade | → hasgrade, grade, feedback | 3.1 |
| mod_quiz_get_attempt_review | attemptid → questions[].html/mark. Needs a finished own attempt with the review option open, else `noreview`. | 3.1 |
| mod_forum_get_forums_by_courses | → type (`news` = announcements) | ≤3.0 |
| mod_forum_get_forum_discussions | forumid, page, perpage | 3.7 |
| mod_forum_get_discussion_posts | discussionid → posts. **Marks posts read when the forum tracks reading.** The user accepted this (D7). | 3.7 |
| gradereport_user_get_grade_items | courseid → itemname, gradeformatted, percentageformatted, feedback, gradeishidden | 3.2 |
| gradereport_overview_get_course_grades | → per-course grade | 3.2 |
| core_calendar_get_action_events_by_timesort | timesortfrom/to, aftereventid, limitnum ≤50 → events (action, overdue) | 3.3 |
| core_calendar_get_calendar_upcoming_view / core_calendar_get_calendar_events | courseid / courseids, timestart, timeend | 3.4 / ≤3.0 |
| message_popup_get_popup_notifications | limit, offset → subject, contexturl, read, unreadcount | 3.2 |
| core_course_get_updates_since / core_course_check_updates | courseid, since → updates{name, timeupdated, itemids} | 3.3 / 3.2 |
| Other modules | lesson 3.3, workshop 3.4, choice, feedback 3.3, glossary/wiki 3.1, data 3.3, h5pactivity 3.9 | |
| mod_lti_* | LTI 1.3 tools (lecture-capture etc.) still need a browser session; out of scope (D17) | |

- **Removed functions:** `mod_forum_get_forum_discussion_posts` (4.0) and `…_discussions_paginated` (4.4).
- **Never call `*_view_*`:** those functions log a view and can set completion.
- **Formatting:** send `moodlewssettingfilter=true` so text filters are applied.

## 3. Files (V M:webservice/pluginfile.php, M:tokenpluginfile.php, M:lib/filelib.php)

- **`webservice/pluginfile.php/{ctx}/{component}/{area}/{itemid}/{path}`** with `token`:
  - It reads `token` from GET or POST. Prefer POST, so the token never appears in a URL; check this live in M3.
  - `forcedownload` is ignored.
  - Add `offline=1` so OneDrive, Google Drive and Nextcloud references return file bytes.
- **`tokenpluginfile.php/{userprivateaccesskey}/…`:** the key **never expires**, and errors come back as HTML. Do not store or use it.
- **Access:** the same `*_pluginfile` rules as the web UI.
- **Size and caching:** there is no size cap. ETag is the file's contenthash, conditional requests return 304, and `Range` works.

## 4. Errors and etiquette (V M:webservice/rest/locallib.php, A:core/classes/errors/wserror.ts)

- **REST errors:** HTTP 200 with `{"exception","errorcode","message"}`. From 4.5 on, `exception` is namespaced, so always match on `errorcode`.
- **token.php and pluginfile errors:** `{"error","errorcode",…}`. A missing file returns 404.
- **Handling:**
  - `invalidtoken` → re-authenticate and notify the user.
  - `accessexception` (expired token, IP restriction, function not allowed) → retry once, then treat as expired.
  - `requireloginerror` → skip that course or activity.
  - Also expect `sitepolicynotagreed` and `sitemaintenance`.
- **Rate:** core has no web-service rate limiter (L). Keep 2–4 requests in flight, sync incrementally, and back off on 429/5xx.

## 5. Prior art

- **moodle-dl:** token.php or a pasted launch URL; autologin cookies for LTI media. Calls site_info, get_users_courses, get_contents and about 20 module getters (D:moodle/request_helper.py).
- **Moodle app:** random passport plus md5 check. Uses SSO only for `typeoflogin` 2/3 or `/auth/oauth2/` identity providers.
- **Moodle MCP servers:**
  - static token or token.php: peancor/moodle-mcp-server, loyaniu/moodle-mcp, haanhtuandev/vgu-mcp (Go);
  - probe + Playwright launch: ink-waffle/moodle-mcp;
  - `confirmed=1` + paste + md5 check: EdmundD28/moodle-codex.

## 6. Decision tree for an SSO + MFA site

1. **Probe** `tool_mobile_get_public_config`.
   - Web services or mobile service off → use the fallback (browser automation, D27).
   - Maintenance mode → retry later.
2. **`typeoflogin=1` and no SSO identity provider** → token.php, without storing the password.
3. **Otherwise**, generate a random passport P and open `launchurl` with `confirmed=1` in the system browser (add `oauthsso` for an OAuth2 provider). Capture `moodlemobile://token=…` by pasting the copied link (M1) or through the HKCU URL handler (M6).
   - If ≥4.3 returns `pluginnotenabledorconfigured`: log out, open launch.php, and log in within 15 minutes.
4. **Verify** the md5, store the token in Credential Manager, and call `core_webservice_get_site_info`. On `invalidtoken`, go back to step 3.

## Open questions (resolve in M1 with the live site)

- `forcedurlscheme` and the real token lifetime can only be observed on the site.
- Whether the university's policy permits third-party use of `moodle_mobile_app` tokens. The user decides; lms-mcp sends an honest `lms-mcp/<version>` user agent and never impersonates the app.
- Whether `webservice/pluginfile.php` accepts the token as a POST field on this site.
