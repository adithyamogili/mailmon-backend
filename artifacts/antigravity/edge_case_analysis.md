# Edge Case Analysis — mailmon

> Covers every layer: scheduler, Telegram webhook, Gmail pool, Redis, OAuth, account lifecycle, on-demand queue.
> Rating: 🔴 High (data loss / silent failure) · 🟡 Medium (duplicate / missed notification) · 🟢 Low (UX / edge)

---

## 1. Scheduler & Cron Recovery

### ✅ COVERED — Server crash resumes at correct time
**Req**: "If the server crashes and restarts, cron resumes on time using persisted `next_run_at` from the database. Overdue runs fire immediately."

`scheduler.Recover()` is called in `main.go` after startup. It calls `userStore.ListEnabled()`, reads each user's `next_run_at`, and calls `StartAt()`. If `next_run_at` is in the past, `delay <= 0` fires after 1ms. ✅ Correctly implemented.

---

### 🔴 OPEN — `last_run_timestamp` lives in Redis, not SQLite
**Req**: "System always fetches emails since `last_run_timestamp`."

`store.RedisStore.SetLastRunTime()` stores the timestamp in Redis with **no TTL** (`0`). But Redis data is volatile. If Redis is flushed, restarts without persistence, or the key is evicted, `GetLastRunTime()` returns `time.Now().Add(-1 * time.Hour)` — only **1 hour of look-back**, not the full gap since last actual run.

**Scenario**: Server runs fine for 3 hours. Redis is restarted without persistence. Next cron fires and only looks back 1 hour → emails from hours 1–2 are permanently missed.

**Fix**: Either persist `last_run_at` to SQLite alongside `next_run_at` (add a column), or set a very long TTL and document the Redis persistence requirement (`appendonly yes` in redis config). The SQLite approach is more robust for this use-case.

---

### 🟡 OPEN — `next_run_at` is NOT updated when interval changes mid-flight
In `HandleUpdateCron`, when `enabled = true` and a new `interval_minutes` is provided, the handler calls `Start(userID, dur)` which **always schedules from now + interval**. This is correct per the requirement ("Changing the interval resets the timer from the update time").

However, `Start()` also calls `persistNextRun()` internally (line 46 in scheduler.go), **and** the handler calls `userStore.UpdateCron()` with `nextRun := time.Now().Add(dur)` before calling `scheduler.Start()`. This means `persistNextRun` is called twice. The second call (inside `Start()`) sets `next_run_at = now + interval` again — a harmless double-write but wasted DB call.

More importantly: if the DB write in `UpdateCron` succeeds but `scheduler.Start()` panics (extremely unlikely), the DB says enabled=true but the in-memory timer was never set. On next restart, `Recover()` will fix this. Low risk, but worth noting.

---

### 🔴 OPEN — `scheduler.fire()` holds the mutex while running `runFn`
```go
func (s *Scheduler) fire(userID string, e *entry) {
    // ...
    s.runFn(context.Background(), userID)   // pipeline runs here — can take up to 5 minutes

    s.mu.Lock()          // acquired AFTER runFn finishes
    defer s.mu.Unlock()
    // ...
    e.timer.Reset(e.interval)
}
```
The mutex is **not** held during `runFn` — good. But between `runFn` returning and acquiring the lock, a concurrent `Stop()` or `Start()` can delete the entry. The code checks `e.stop` after re-acquiring the lock, so that's handled. ✅ Correct.

However, `e.timer.Reset(e.interval)` is called on a timer that `Stop()` may have already called `.Stop()` on. Calling `Reset` after `Stop` is safe in Go 1.23+ but could fire an extra tick in older runtimes. **Verify Go version** is ≥ 1.23.

---

### 🟡 OPEN — No upper bound on missed cron runs
If the server is down for 48 hours, on recovery every enabled user fires immediately and simultaneously. With 100 users that's 100 parallel pipeline runs, each fetching 48-hours worth of emails, each potentially hitting the LLM. This is a thundering herd on startup.

**Fix**: Spread recovered overdue runs with a staggered delay (e.g., `i * 2 * time.Second`) rather than all at `1ms`.

---

## 2. Telegram Webhook — Idempotency & Error Handling

### 🔴 OPEN — `w.WriteHeader(http.StatusOK)` is sent before processing
```go
// webhook/handler.go line 59
w.WriteHeader(http.StatusOK)

// Handle /link command
if strings.HasPrefix(text, "/link ") { ... }
```
The `200 OK` is written **unconditionally before any business logic** for the `/link`, `/start`, and on-demand flows. This is intentional to prevent Telegram from retrying, but it has a critical consequence:

**If `handleLink` fails after line 59** (e.g., DB is down, `UpdateTelegramChatID` fails), the user sees "Failed to link. Try again." in Telegram, but re-sending `/link <code>` will fail because `ResolveLinkCode` already **deleted the Redis key** (line 293 in handlers.go: `h.rdb.Del(ctx, key)`) before `UpdateTelegramChatID` was called.

**The user is now stuck**: the code is consumed but Telegram is not linked. They must go back to the website, generate a new code, and try again — but there's no error message telling them to do that. They only see "Failed to link. Try again." which implies re-sending the same code will work.

**Fix**: Delete the Redis link code **only after** `UpdateTelegramChatID` succeeds. Move the `Del` to after the DB update inside `handleLink` (not in `ResolveLinkCode`). Change `ResolveLinkCode` to a two-step: peek (GET) vs consume (DEL).

---

### 🟡 OPEN — On-demand enqueue failure is silent to user
```go
// webhook/handler.go lines 88–92
info, err := h.asynqClient.Enqueue(task)
if err != nil {
    slog.Error("webhook: enqueue failed", "err", err)
    return  // ← user gets NO response
}
```
Telegram has already received `200 OK` (line 59), so it won't retry. But the user gets no Telegram message telling them the request failed. They'll just wait indefinitely.

**Fix**: On enqueue failure, `h.notifier.Send(ctx, chatID, "Couldn't process your request right now. Please try again in a moment.")`.

---

### 🟡 OPEN — On-demand task is not deduplicated at the queue level
If a user sends two messages rapidly (or Telegram retries the webhook), two `TypeProcessOnDemand` tasks are enqueued for the same user. Both will run and send two identical summaries within seconds of each other.

**Fix**: Use `asynq.TaskID(...)` with a user-scoped ID (e.g., `"ondemand:" + u.ID`) and `asynq.Unique(5 * time.Minute)` to deduplicate within a time window. Asynq supports this natively.

---

### 🟢 LOW — `/link` with trailing spaces in code
```go
code := strings.TrimSpace(strings.TrimPrefix(text, "/link "))
```
`TrimSpace` handles this. ✅

---

### 🟢 LOW — Unrecognized commands get on-demand treatment
Any message that isn't `/link ...` or `/start` triggers the on-demand flow. If the user sends `/help` or `/status`, they get a job email summary instead of help text. Not a crash, but poor UX.

---

## 3. Gmail Pool — Restart & Token State

### 🔴 OPEN — Gmail pool is not loaded for users with Gmail but no cron
`main.go` loads the pool only for users in `ListEnabled()` (i.e., `cron_enabled = 1 AND gmail_token != ''`). But a user can have Gmail connected with cron disabled. If that user sends an on-demand message, `p.gmailPool.Get(userID)` returns `false`, and `RunOnDemand` returns an error — the user gets no response (the worker returns an error, asynq retries 3 times, then dead-letters it).

**Fix**: In `main.go`, load Gmail clients for **all users with a non-empty `gmail_token`**, not just enabled ones. Add a `ListWithGmailToken()` method to `user.Store`.

---

### 🟡 OPEN — OAuth token refresh failure is silent
In `NewClientFromToken`, if the token refresh callback errors:
```go
if newJSON, err := gmail.TokenToJSON(newTok); err == nil {
    userStore.UpdateGmailToken(...)  // error ignored
}
```
The `UpdateGmailToken` result is discarded. If this fails, the new token is lost. Next restart will load the stale token; if it's expired, the pipeline will fail with a 401 on every run.

**Fix**: Log the error from `UpdateGmailToken` in the `onRefresh` callback.

---

### 🟡 OPEN — Expired/revoked token causes cron to silently skip forever
If a user revokes Gmail access from Google's side, `FetchSince()` will return a 401 error. `RunScheduled` logs the error and returns — the scheduler reschedules normally. The next run will fail again. This repeats indefinitely.

There's no mechanism to: (a) notify the user their Gmail connection is broken, (b) disable cron automatically, (c) surface the error in the UI (`/api/auth/me` only exposes `gmail_connected: true/false` based on token presence, not token validity).

**Fix**: On repeated 401s from Gmail, set `cron_enabled = false`, clear the token, and send a Telegram message: "Your Gmail connection expired. Please reconnect at the website."

---

## 4. Redis Failure Scenarios

### ✅ COVERED — Dedup failure is non-fatal
Requirements state: "Redis dedup failures are non-fatal — user may get a duplicate notification rather than missing one."

`pipeline.go` line 78–80: dedup check failure logs a warning and continues. ✅ Matches requirements.

---

### 🔴 OPEN — `SetLastRunTime` failure means emails are re-processed next run
```go
// pipeline.go lines 67–69
if err := p.store.SetLastRunTime(ctx, userID, time.Now()); err != nil {
    slog.Error("pipeline: failed to update last run time", ...)
}
// continues — no return
```
If Redis is down and `SetLastRunTime` fails, `last_run_at` is not updated. Next run will re-fetch all the same emails from the previous `last_run_at`. The dedup layer (`IsNotified`) will catch already-notified ones, but emails that passed keyword filter and LLM but were marked `relevant: false` will be re-classified (wasted LLM calls). More critically, if `IsNotified` also fails (Redis still down), the user gets duplicate notifications for all relevant emails.

This is a cascading failure: Redis down → `SetLastRunTime` fails → `IsNotified` fails → duplicate Telegram messages for every relevant email on every cron tick.

**Fix**: This is somewhat unavoidable without a persistent fallback for `last_run_at`. Moving it to SQLite (see issue #2) eliminates this entire class of failures.

---

### 🟡 OPEN — Link code `Set` failure is silently ignored
```go
// handlers.go line 280
h.rdb.Set(r.Context(), "linkcode:"+code, userID, 10*time.Minute)
```
The error from `rdb.Set` is ignored. If Redis is down, the code is returned to the user but can never be resolved. The user tries `/link <code>` and gets "Invalid or expired code."

**Fix**: Check the error and return a 500 to the frontend if the Set fails.

---

## 5. Account Lifecycle Race Conditions

### 🟡 OPEN — Delete account while pipeline is running
`HandleDeleteAccount` calls `scheduler.Stop(userID)` then `userStore.Delete()`. But if the scheduler already fired and `RunScheduled` is mid-execution (e.g., mid-LLM call), it holds no locks on the user. The pipeline will complete and try to:
1. Call `notifier.Send()` with a now-deleted user's chat ID — harmless, Telegram just sends to the chat.
2. Call `dedup.MarkNotified()` — writes to Redis, no-op after delete.
3. Call `store.SetLastRunTime()` — writes to Redis for a deleted user, orphaned key.

No crash, but orphaned Redis keys. Low severity since data is bounded by TTL.

---

### 🟡 OPEN — Gmail disconnect doesn't stop an in-flight scheduled run
`HandleGmailDisconnect` calls `scheduler.Stop()` and `gmailPool.Remove()`. If the scheduler fired 1 second ago and the pipeline is currently calling `gmailClient.FetchSince()`, the `Remove()` only removes the pool entry — the already-obtained `*Client` reference is still valid and the fetch completes. The subsequent `notifier.Send()` will run normally. Harmless but worth knowing.

---

### 🟢 LOW — Telegram re-link changes chat ID without notifying old chat
If a user runs `/link <code>` from a different Telegram account, `UpdateTelegramChatID` silently overwrites. The old account gets no notification that it was unlinked. The new account gets "Linked to ...". Acceptable UX, but worth noting.

---

## 6. LLM / Classifier Edge Cases

### ✅ COVERED — 3x exponential backoff
`groq.go` implements backoff: `[1s, 2s, 4s]`. ✅

### 🟡 OPEN — `context.WithTimeout` in pipeline vs. backoff sleep

`RunScheduled` sets a 5-minute pipeline timeout. The Groq classifier can sleep up to `1+2+4 = 7 seconds` total across retries. This is fine. But `time.Sleep(wait)` in `groq.go` does **not** respect the context — if the pipeline's context is cancelled (e.g., the 5-minute timeout fires mid-retry sleep), the sleep continues for up to 4 more seconds before the next `http.NewRequestWithContext` call propagates the cancellation.

**Fix**: Replace `time.Sleep(wait)` with:
```go
select {
case <-time.After(wait):
case <-ctx.Done():
    return nil, ctx.Err()
}
```

---

### 🟡 OPEN — LLM response count mismatch drops all results for that batch
`parseBatchClassification` returns an error if `len(results) != expected`. If the LLM returns fewer items than sent (e.g., merges two emails), **all emails in that batch are dropped** and re-classified next cycle. The re-classification will hit cache for none of them (since `CacheResult` was never called). This could loop if the LLM consistently merges emails.

There's no partial success path — it's all-or-nothing per batch.

**Fix**: Consider falling back to per-email classification when batch count mismatches, or accept partial results up to `min(len(results), expected)`.

---

### 🟢 LOW — Emails with no body preview pass keyword filter vacuously
If `BodyPreview` is empty (multipart email with only attachments), it matches nothing in body. It can still match subject or From. Expected behavior.

---

## 7. Auth & JWT Edge Cases

### 🟡 OPEN — JWT has no expiry enforcement visible in code
`CreateJWT` and `AuthMiddleware` are not shown in the read files. If JWT tokens are long-lived (e.g., 30 days) and there's no revocation mechanism, a deleted user's JWT remains valid until expiry. Between account deletion and JWT expiry, the same JWT can call authenticated endpoints — they'll return 404 (user not found) for most, but the JWT itself is accepted.

**Status**: Need to verify JWT expiry in `api/jwt.go` (file not shown in this review). Flag for check.

---

### 🟢 LOW — Google ID token verification uses no clock skew tolerance
If verifyGoogleIDToken does not allow for clock skew, tokens issued by Google up to a few seconds ago might fail verification on a server with slightly drifted clock.

---

## 8. HTTP / API Layer

### 🟢 LOW — No rate limiting on `/api/telegram/link-code`
A logged-in user can call this endpoint unlimited times, generating many Redis keys (`linkcode:*`). Each expires in 10 minutes, so this is bounded, but a user could spam ~1000 codes/minute. A simple per-user rate limit (1 code per 30 seconds) is sufficient.

---

### 🟢 LOW — `HandleUpdateCron` checks `GmailToken != ""` but not that Gmail client is in pool
After a server restart, if `gmailPool.AddFromToken` fails for a user (token parse error), their Gmail client is not in the pool. But `HandleUpdateCron` only checks `u.GmailToken != ""` before enabling cron. Cron starts, fires, `pipeline.classifyEmails` calls `gmailPool.Get()` → returns `false` → returns error → `RunScheduled` logs and skips. Silent failure.

**Fix**: The pool-load error in `main.go` should set `cron_enabled = false` and clear the token for affected users on startup.

---

## 9. Graceful Shutdown

### ✅ COVERED — Shutdown sequence
`main.go` on SIGINT/SIGTERM:
1. `httpServer.Shutdown(10s)` — stops accepting new requests, waits for in-flight
2. `sched.StopAll()` — cancels all timers
3. `asynqServer.Shutdown()` — waits for active workers
4. Closes asynq client and Redis

The scheduler timers that already fired but whose `runFn` is mid-execution (e.g., pipeline running) will **not** be interrupted — `context.Background()` is passed to `runFn`, not a cancellable context derived from a shutdown signal. So the server could take up to 5 minutes (pipeline timeout) to fully shut down even after SIGTERM.

**Fix**: Pass a root context to the scheduler that is cancelled on shutdown. This is a meaningful improvement for containerized deployments.

---

## Summary Table

| # | Area | Issue | Severity | Covered? |
|---|------|-------|----------|----------|
| 1 | Scheduler | Crash recovery with `next_run_at` | ✅ | Yes |
| 2 | Redis | `last_run_timestamp` lost on Redis restart | 🔴 | **No** |
| 3 | Webhook | Link code consumed before DB write | 🔴 | **No** |
| 4 | Webhook | Enqueue failure → silent to user | 🟡 | **No** |
| 5 | Webhook | On-demand task not deduplicated | 🟡 | **No** |
| 6 | Gmail Pool | Pool not loaded for non-cron users | 🔴 | **No** |
| 7 | Gmail Pool | Token refresh error silently ignored | 🟡 | **No** |
| 8 | Gmail | Revoked token loops silently forever | 🟡 | **No** |
| 9 | Redis | `SetLastRunTime` failure → duplicate notifs | 🔴 | **No** |
| 10 | Redis | Link code `Set` error ignored | 🟡 | **No** |
| 11 | Account | Delete while pipeline in-flight | 🟢 | Partial |
| 12 | LLM | `time.Sleep` ignores context cancellation | 🟡 | **No** |
| 13 | LLM | Batch count mismatch drops all results | 🟡 | **No** |
| 14 | Auth | JWT expiry / revocation after delete | 🟡 | Unverified |
| 15 | Scheduler | Thundering herd on recovery | 🟡 | **No** |
| 16 | Shutdown | `runFn` not cancellable on SIGTERM | 🟢 | **No** |
| 17 | API | No rate limit on link-code generation | 🟢 | **No** |
| 18 | API | Pool state not validated before cron enable | 🟡 | **No** |
