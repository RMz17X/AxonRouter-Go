## CI integration job: SASL auth failure — the password existed twice and disagreed

**Symptom.** The first CI run with the PostgreSQL service (fragment
953) failed all 291 integration tests at 0.01s each:
"password authentication failed for user postgres (SQLSTATE 28P01)".

**Root cause.** Fragment 953's workflow carried the scratch
database's password in two places — the service container's
POSTGRES_PASSWORD and the INTEGRATION_DATABASE_URL — and the two
values were different strings. The service initialized with one, the
tests connected with the other. Local verification could not catch
it: the local scratch database predates the workflow and its
password lives in a single env file.

**Fix.** The duplicated value is deleted rather than synchronized.
The ephemeral, runner-local scratch database now uses
POSTGRES_HOST_AUTH_METHOD: trust and a passwordless DSN — there is
nothing left to disagree. The job comment records the reasoning.
Also: the test step's timeout rises 10m → 25m; the suite measured
646s locally WITHOUT -race on a loaded 4-vCPU VM, so 10m under
-race was never a safe ceiling for a green run.

**Verification.** The exact failure string comes from the CI run's
own log (job 127448646859); the fix removes the mechanism (two
values) rather than patching one value against the other. The
follow-up CI run on main is the end-to-end proof.
