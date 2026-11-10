---
type: fix
---

Usage day boundaries now follow the process-local timezone (the standard `TZ` environment variable) instead of hardcoded UTC — a deliberate divergence from upstream, which keys usage days in UTC. The daily rollup writer (including the error-fold key), the usage chart's day bucketing, and the dashboard streak/heatmap cursors all derive their `2006-01-02` day key in local time, matching the `period=today` anchor that already used the local calendar. An operator who runs the gateway with `TZ=Asia/Jakarta` now sees WIB days consistently in the summary cards, the daily chart, and streaks; with `TZ` unset the container default (UTC) keeps the previous behaviour. Stored timestamps remain UTC instants; only the calendar-day keying moved. Rollup rows written before this change keep their UTC keys, so the transition day's history can appear split across two day rows until the window rolls past it.
