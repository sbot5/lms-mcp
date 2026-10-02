# Ed API notes (student view)

Research snapshot from 2026-10. Ed publishes no real API documentation: its token page says "API access is currently in beta. APIs may change without notice." Nothing here was called live. Confidence tags:

- **V**: seen in the code or notes of a client whose author reports live use (linked).
- **L**: one client only, or older evidence.
- **I**: inference.

## Base URL and auth

- Regions:
  - AU: `https://edstem.org/api`. The user's courses are here, and lms-mcp v0.3.0 ran against it successfully.
  - US: `https://us.edstem.org/api`.
  - EU: `https://eu.edstem.org/api`.
  - `au.edstem.org` 302-redirects to `edstem.org`. IDs and hosts do not cross regions. **V** [sdk][eli][push]
- Header: `Authorization: Bearer <token>`. A bad token returns 400 `{"code":"bad_token"}` (L).
- Ed's token page calls the token "a substitute for your password… on your behalf". The page shows no scope or expiry, so treat the token as a full-account credential. lms-mcp must enforce read-only access itself.
- Cloudflare rejects some default User-Agents (403 / error 1010) [push][sdk]. Send an explicit `User-Agent: lms-mcp/<version>`.
- No rate limits are documented, and no client reports API 429. Keep about 10 req/s or less with Retry-After handling.

## Endpoints (all GET, relative to the base URL)

| Path | Purpose | Key fields | Conf |
|---|---|---|---|
| `/user` | me + enrolments | `user{id,has_pats}`; `courses[{course{id,code,name,year,session,features.resources/lessons/exams,settings.discussion.categories}, role.role}]` | V [edapi] |
| `/courses/{c}/resources` | Resources tab (staff files, 课件) | `resources[{id,name,extension,category,session,size,link,embedding,staff_only,release_at,created_at,updated_at}]` | L [xt][cc] |
| `/resources/{r}/download/{name+ext}?dl=1` (or `/download?dl=1`) | file bytes; needs auth | Content-Disposition, Content-Length. Redirect behaviour unknown. | L [xt][cc] |
| `/courses/{c}/lessons` | lessons + modules (`slides` always `[]`) | kind, state (active/scheduled), status (unattempted/attempted/completed), available_at, due_at, locked_at, solutions_at, effective_*, openable, attempts, is_timed, last_viewed_slide_id | V [sdk][cli] |
| `/lessons/{l}` | lesson with slides | slide `type`, status, content (Ed XML), `file_url` (PDF slides), video_url, url, html, passage, challenge_id, auto_points | V [sdk][pack] |
| `/lessons/slides/{s}/questions` | quiz questions | `data{type,content,answers,explanation,solution (when released)}` | V [tk][pack] |
| `/lessons/slides/{s}/questions/responses` | own quiz answers | question_id, data, correct, created_at | V [pack][cli] |
| `/challenges/{ch}` | code challenge | content, explanation. **Also returns workspace tickets/JWTs: never store or log them.** | V [tk] |
| `/users/{me}/challenges/{ch}/submissions` | own code submissions | status, is_completed, testcase_pass_count/total_count, feedback_provided, lesson_mark_id | V for staff; I for students |
| `/lessons/{l}/attempts/{me}` → `/attempts/{a}/quiz_responses/{s}` → `/lesson_marks/{m}?rubric_items=true` | lesson marks | lesson_mark{auto_mark,rubric_mark,mark_override,comment} | V for staff; I for students |
| `/courses/{c}/threads` | thread list | `limit`≤100. Paginate with `offset` or the `sort_key` cursor; `sort=new\|trending`; `filter=unread\|new\|unanswered\|unresolved\|watching\|starred\|mine\|private\|staff`; `category`; `q`. Items carry is_seen, glanced_at, new_reply_count, is_starred, is_watched, vote. | V [sdk][edapi] |
| `/courses/{c}/threads/search?query=` | server-side search | total, threads with highlights; sort=relevance\|newest\|oldest | V [sdk] |
| `/courses/{c}/threads/{number}` | thread by its #number | thread, users | V [edapi] |
| `/threads/{t}` | thread detail | answers, comments (nested), users. `?no_comments=1` exists. | V [sdk] |
| `/users/{me}/profile/activity?courseID=&filter=all\|thread\|answer\|comment&limit≤50&offset=` | own posts | items[{type,value}] | V [edapi] |
| `wss://…/api/stream`, send `{type:"course.subscribe",oid:c}` | push events (thread/comment new/update/delete) | | L [edpy] |
| `static.{au,us,eu}.edusercontent.com/files/{id}` | `file_url`, `<file url>`, `<image src>` | fetched without auth; no redirect expected | L [cli][pack] |

## Facts that affect the design

- **Side effects to avoid:** `?view=1` on lessons records progress. Starring is a POST. Never send either.
- **Unverified side effect:** whether `GET /threads/{t}` marks a thread as read. v0.3.0 already calls it, so check it during M1 live acceptance (see Open questions).
- Announcements are threads with `type:"announcement"`; they have no separate endpoint. Discussion categories come from the course settings in `/user`.
- There is no `since` parameter. For incremental sync, page `sort=new` until a stored watermark. Pinned threads may stay first regardless of sort. Reconcile with a periodic full scan.
- List responses have no total and no has_more. Offset paging drifts when new threads arrive, so prefer `sort_key`.
- No gradebook or exam endpoint was found for students. Grades probably reach the university LMS through LTI. Ed lesson marks are verified only for staff.
- Slide types: document, pdf, video, quiz (including survey), code/jupyter/rstudio/postgres/web/karel (each backed by a challenge), webpage, html, codecast, workspace-partition. Attachments appear as `<file url filename>` and `<image src>` inside the content XML.
- Polls are a block inside documents; no API for them is known.

## Open questions (verify with the dev token during M1 live acceptance; T = token, B = base URL)

1. Resource download auth and redirects: `GET $B/resources/{r}/download?dl=1` with T (headers only).
2. Static files are public: fetch a `file_url` without auth and check the status.
3. Students can read their own marks: `GET $B/users/{me}/challenges/{ch}/submissions`.
4. `GET /threads/{t}` marks a thread as read: compare `GET $B/courses/{c}/threads?filter=unread` before and after. This changes read state once, so ask the user first.
5. Students can use search: `GET $B/courses/{c}/threads/search?query=exam&limit=5`.
6. The user's course `code`, `year` and `session` formats, needed to detect the current semester and pair courses with Moodle.

## Other clients worth reading

- bunizao/edstem-cli (most complete; unread catch-up, attachments, own quiz responses, activity): https://github.com/bunizao/edstem-cli
- KarlRombauts/edstem-sdk (regions, threads, search, filters, marks): https://github.com/KarlRombauts/edstem-sdk
- smartspot2/edapi docs: https://github.com/smartspot2/edapi/blob/master/docs/api_docs.md
- imosleo/edpack (quiz answers, webpage slides): https://github.com/imosleo/edpack
- Resources sync: https://github.com/xiaotianxt/skills/blob/main/skills/edstem/scripts/sync_edstem.py, https://github.com/xiaojiou176-open/campus-copilot/tree/main/packages/adapters-edstem
- Ed MCP servers: eliemada/edstem-mcp (lessons), RichieRish05/EdStemMCP (search + rerank); rob-9, 1jehuang, k3arjun and wondermuttt cover discussion only.
- Websocket stream: https://github.com/bachtran02/edpy
- Regional hosts and the Cloudflare User-Agent issue: https://github.com/purduecsbridge/ed-push/blob/main/TROUBLESHOOTING.md, https://github.com/eliemada/edstem-mcp/issues/2
- Marks endpoints: https://github.com/jspaniac/BackreadingBot/blob/main/src/ed_helper.py, https://github.com/TimKingtonFC/EdTools
