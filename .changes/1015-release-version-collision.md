# 1015 — Release version collision correction (v1.9.42 → v1.9.43)

## Symptom

Two release commits named v1.9.42 landed on main in the same hour: the
tokenharbor release (tagged v1.9.42) and the provider-account-health release
(fragment 1014), whose bump targeted 1.9.42 because its base already carried
that version. The v1.9.42 tag therefore points at the tokenharbor release
commit, which does not contain fragment 1014.

## Fix

This release re-issues the combined main tip as v1.9.43 (sequential PATCH+1
from the taken 1.9.42, per the repo versioning law). No code changes: main's
content is unchanged apart from the version stamps and this note. The
fragment-1014 changelog entry stays under [v1.9.42], where it was folded;
the v1.9.43 tag is the first tagged release containing it.
