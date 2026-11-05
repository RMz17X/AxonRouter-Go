## Handler tests realigned with the behaviour they pin

Three unit tests had drifted from deliberate behaviour and failed in CI:

- TestHandleUsageStream assumed the first SSE frame is `data:`; the
  usage stream deliberately opens with a `retry: 3000` reconnect hint.
  The test now reads to the first data frame.
- TestSuggestedModelsBadRequests pinned "unreachable feed → 200 with
  empty data" using `type=mimo-free`, whose filter was removed with
  that provider (unknown types are the 400 case the same test pins).
  The leg now uses a live filter (opencode-free); the 200-empty
  contract is unchanged.
- TestAntigravityQuota_Generic429NoStrike still expected upstream
  #3684's three-strike rule for definitive quota errors. This fork
  deliberately blocks a definitive quota error (QUOTA_EXHAUSTED /
  "Individual quota reached") immediately — "instant quota 429
  rotation", commit 4bc8f72f. The test now pins the fork contract:
  generic/content 429s never strike (the #4197 core, unchanged),
  definitive quota errors block on the first, and non-definitive
  explicit errors (rate-limit markers) still use three strikes. The
  divergence is recorded in the test's doc comment.
