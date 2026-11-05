## Web unit tests realigned with the Usage page's current design

The bun suite had eight failures, all tests pinning a superseded
design or a machine-specific assumption:

- usagePanelPlan.test.ts still arranged `overview.*` panels and
  expected blocksFor to pair the topology+recent row. The Overview
  section is a fixed Axon-layout mirror with no arrangeable panels
  (the panel universe covers Details/Cache/Compression), and blocksFor
  has emitted one block per panel since. The tests now pin the current
  design on the Details group: order repair, the Default preset's
  three hidden cards, move/moveBefore semantics, and blocksFor's
  one-block-per-panel + duplicate collapse.
- consoleLog.test.ts built a toPlainText expectation from a UTC-parsed
  instant while formatClock renders local time by design, so the test
  only passed on UTC machines. It now constructs local-time dates like
  the formatClock tests do.

The full web suite passes: 774 pass / 0 fail (was 766 / 8).
