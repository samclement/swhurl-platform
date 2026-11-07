# Dashboard reconciler fixtures and the Python case map

- `definitions.json`: `dashboards.dashboard()` of the Python job for a web app (`staging`, `prod`)
  and a worker (`staging`), sources `t1` and `l1`. `Definition` must produce the same documents.
- `webhooks.json`: `clickstack_ntfy.destination()` and `notify_destination()` for the topic
  `swhurl-test-private-topic-012345` and the secret `private-alert-secret` (synthetic). `Destinations`
  must produce the same bytes, so the Go pass does not rewrite a webhook the Python job wrote.

Both were produced by the retired Python job (deleted 7 November 2026, recoverable from Git before it) and are
now the contract itself; neither is a live export.

## All 40 Python cases

`tests/test_dashboards.py` (37) and `tests/test_clickstack_ntfy.py` (3), both deleted with the Python job; the
job's manifest and Secret RBAC checks moved to `tests/test_images.py`. Where a case moved to a
deliberate change, the Go test named covers the new behaviour.

| # | Python case | Go coverage |
| --- | --- | --- |
| 1 | an app gets one alert per preset on its dashboard's tiles | `dashboards.TestAnAppGetsOneAlertPerPresetOnItsDashboardsTiles`, `orchestration.TestAnAppGetsItsDefaultAlertsThroughTheNotifyWebhook` |
| 2 | a job has its error-logs alert but no errors alert | changed: no job dashboards; `dashboards.TestAJobOrAnUnreadableDocumentStopsDiscovery`, `TestAppsComeFromTheRecordsOrNothingIsDone` |
| 3 | alerts that match are left; the rest updated in place or deleted | `dashboards.TestAlertsThatMatchAreLeftAndTheRestAreUpdatedInPlaceOrDeleted`, `orchestration.TestAlertsAreUpdatedInPlaceOrDeleted` |
| 4 | a missing notify webhook is created before alerts using its new id | `orchestration.TestMissingWebhooksAreCreatedBeforeAlertsWithOneAuthentication` |
| 5 | a dry run and a cluster with no app change nothing | `orchestration.TestADryRunWritesNothing`, `TestEmptyDiscoveryRepairsWebhooksAndPrunesNothing` |
| 6 | an app sets its own thresholds or switches alerts off | `dashboards.TestAnAppSetsItsOwnThresholdsOrSwitchesAlertsOff`, `orchestration.TestTheClusterJobReadsAlertSettingsFromNamespaces` |
| 7 | settings are parsed strictly and written as short as they can be | `dashboards.TestSettingsAreParsedStrictly`; `spec_of` has no caller and is not ported |
| 8 | the cluster job reads an app's settings from its namespaces | `dashboards.TestTheClusterJobReadsAnAppsSettingsFromItsNamespaces`, `orchestration.TestTheClusterJobReadsAlertSettingsFromNamespaces` |
| 9 | an alert's name says which app and preset it is | `dashboards.TestAnAlertsNameSaysWhichAppAndPresetItIs`; the reading side is `notify` (`alert_test.go`) |
| 10 | creates, keeps, updates and deletes by app | `dashboards.TestPlanCreatesKeepsUpdatesAndDeletesByApp`, `orchestration.TestDashboardsAreCreatedKeptUpdatedAndDeletedByApp` |
| 11 | each app's dashboard is recorded for the console, written only when it changed | `orchestration.TestTheRecordIsWrittenOnlyWhenItChangedAndAFailedWriteStopsThePass`, `dashboards.TestRecordIsThePythonBytesOfValidEntries` |
| 12 | only an app name and a dashboard id are read back | the reader is the console's (`dashboards.recorded`) and stays with it; the writer records only those: `dashboards.TestRecordIsThePythonBytesOfValidEntries` |
| 13 | a record that cannot be written fails the run | changed: it stops the pass before the alerts; `orchestration.TestTheRecordIsWrittenOnlyWhenItChangedAndAFailedWriteStopsThePass` |
| 14 | dry run reads but writes nothing | `orchestration.TestADryRunWritesNothing` |
| 15 | no access key stops before the API | `orchestration.TestNoAccessKeyStopsBeforeTheAPI`, `clickstacktest.Contract` |
| 16 | tiles filter on the app's namespaces and split by environment | `dashboards.TestTilesFilterOnTheAppsNamespacesAndSplitByEnvironment`, `TestDefinitionsAreWhatThePythonJobSent` |
| 17 | a job's dashboard shows its runs | removed with job dashboards (v2 runs no job) |
| 18 | server defaults do not count as a change | `dashboards.TestServerDefaultsDoNotCountAsAChange`, `TestAnUnexpectedTileShapeIsADifferenceNotAFailure` |
| 19 | apps come from records even without valid rendered output | changed: v2's records; `dashboards.TestAppsComeFromTheRecordsOrNothingIsDone`, `orchestration.TestBadRecordsStopThePass`, `cmd.TestClickStackDashboardsRecordsAreReadBeforeTheCluster` |
| 20 | creation and first promotion update the existing dashboard | `orchestration.TestEnvironmentsJoiningOrLeavingUpdateTheSameDashboard` |
| 21 | discovery excludes shared services and mismatched names | `dashboards.TestDiscoveryExcludesSharedServicesAndMismatchedNames` |
| 22 | a removed app loses its dashboard and others are untouched | `orchestration.TestTheClusterPassDeletesOnlyDashboardsNamedForAGoneApp` |
| 23 | removing one environment updates and never deletes | `orchestration.TestEnvironmentsJoiningOrLeavingUpdateTheSameDashboard` |
| 24 | the scheduled sync deletes only tagged dashboards named for an app | `orchestration.TestTheClusterPassDeletesOnlyDashboardsNamedForAGoneApp`, `dashboards.TestPlanCreatesKeepsUpdatesAndDeletesByApp` |
| 25 | a scheduled dry run lists the deletion and writes nothing | `orchestration.TestTheClusterPassDeletesOnlyDashboardsNamedForAGoneApp` (dry) |
| 26 | empty discovery never deletes dashboards | `orchestration.TestEmptyDiscoveryRepairsWebhooksAndPrunesNothing` |
| 27 | a discovery failure stops before API writes | `orchestration.TestDiscoveryFailureStopsBeforeTheAPI` |
| 28 | create, repair, no-op, stable ids and unrelated bytes | `orchestration.TestWebhooksAreCreatedRepairedAndKeptWithStableIDs`, `dashboards.TestWebhooksAreCreatedRepairedInPlaceOrKept` |
| 29 | both reserved names are preflighted before any webhook write | `dashboards.TestBothReservedNamesArePreflightedBeforeAnyWebhookWrite`, `orchestration.TestAConflictOrATruncatedListingWritesNoWebhook` |
| 30 | a truncated listing prevents create and repair for both names | `orchestration.TestAConflictOrATruncatedListingWritesNoWebhook`, `clickstacktest.Contract` |
| 31 | empty discovery repairs webhooks without pruning dashboards, alerts or record | `orchestration.TestEmptyDiscoveryRepairsWebhooksAndPrunesNothing` |
| 32 | the first run uses one authentication and one list before alerts | `orchestration.TestMissingWebhooksAreCreatedBeforeAlertsWithOneAuthentication`, `clickstack.TestProgramsAreStated` |
| 33 | dry-run missing and changed webhooks are redacted and write nothing | `orchestration.TestADryRunWritesNothing` (every report is checked for credentials) |
| 34 | missing, empty or invalid credentials stop without webhook writes | `orchestration.TestBadCredentialsStopBeforeAnyWebhookWrite`, `kube.TestSecretGetIsNamedAndFailsClosed` |
| 35 | an invalid topic is not echoed and stops before webhook writes | `dashboards.TestInvalidCredentialsMakeNoWebhook`, `orchestration.TestBadCredentialsStopBeforeAnyWebhookWrite` |
| 36 | API errors fail redacted without later reconciliation | `orchestration.TestAPIErrorsFailWithoutLaterReconciliation`, `clickstack.TestFailuresNeverRepeatCredentialsOrAnswers` |
| 37 | the job keeps its bound image and exact Secret RBAC | a cutover gate: the manifests do not change offline. `cmd.TestClickStackDashboardsClusterPassReadsByNameAndStopsAtARefusedExec` pins the requests up to the first exec (selected lists, four named Secret GETs); the Deployment get, pod list, app exec and the record's get and patch are checked against `dashboard-rbac.yaml` by review and by the cutover's live dry run |
| 38 | the notify service reads what the notify webhook sends | `dashboards.TestTheWebhooksAreThePythonJobsAndTheNotifyServiceReadsThem` (same fixture as `notify`) |
| 39 | the platform webhook sends what a notice sends | `dashboards.TestTheWebhooksAreThePythonJobsAndTheNotifyServiceReadsThem` (Python's bytes) |
| 40 | the dashboard command loads without httpx | not applicable: the Go binary has no optional runtime dependency |
