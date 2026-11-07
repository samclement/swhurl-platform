# Host heartbeat fixtures and legacy-case map

This is a test-fixture contract, not a progress log. The Python heartbeat (`tools/swhurl/notifications/heartbeat.py`)
still runs until the cutover; this page maps its tests to the Go tests that keep its behaviour. See
[the approved contract](../../../docs/iteration-2-plan.md#15-the-host-heartbeat-in-go-approved-7-november-2026).

## Provenance

The three state files were written by Python's own `save_state` (the checkout at `c801f707`, unchanged), called once
per file. The clock is 3 October 2026, 12:00 UTC (`1791028800.0`), as in the Python tests:

| File | State given to `save_state` |
| --- | --- |
| `python-alerting.json` | the checker alerting since the clock, last alerted 3600.25 s later |
| `python-cleared.json` | no flags at all (`{}`), which Python writes as the checker's empty flag |
| `python-others.json` | the checker cleared, `a/b` cleared and `reports/sync` alerting, inserted in that (sorted) order |

`heartbeat.TestPythonFilesRoundTripByteForByte` decodes each file and checks that Go encodes the same bytes.
`adapters/hoststate/testdata/notification-heartbeat.json` is a copy of `python-alerting.json`: the contract suite
reads it through both the real adapter and the fake. No address, topic or Secret appears in any fixture.

## Intentional differences

The contract's three approved differences (`--state`, `--max-age` kept, the unit's `PATH`) are not repeated here.

1. **Numbers:** every time is written as Python writes a float (`repr`): `12` read from a file is written back as
   `12.0`, `1e16` as `1e+16`. Python wrote an integer it had read back as an integer. Both programs read either.
2. **Order:** a flag's fields are always `alerting_since`, `last_alert`, and the keys under `others` are sorted.
   Python kept a file's own order and appended new jobs. Neither reader depends on order. Keys are escaped as
   Python's `ensure_ascii` does, so a document Go writes from Python's values is Python's bytes.
3. **`lastSuccessfulTime`** must be RFC 3339, as Kubernetes writes it. Python's `fromisoformat` also read a time
   without an offset (as local time) or a bare date; Go calls those invalid, which is stale.
4. **A CronJob document Go cannot read** (`spec` or `status` of the wrong type, `suspend` not a boolean) is
   `<label> status is invalid`: exit 1 and nothing sent. Python used truthiness or stopped with a traceback.
5. **A CronJob is missing** only when the API server answers 404 (`kube.Read`'s absence); Python searched `kubectl`'s
   message for "not found". Any other failure, a refused request included, is `cannot read <label>`.
6. **The state file:** one over 1 MiB is unreadable; Python read any size. `NaN` and `Infinity`, which Python's
   parser accepted and its validation then refused as invalid, are unreadable JSON to Go. Both exit 1 and leave the
   file unchanged; only the message differs. The temporary file is `.notification-heartbeat.json.*`, not
   `.notification-heartbeat-*`. A missing state directory's missing parents are made 0700 too; Python made only the
   last one 0700.
7. **Messages:** a refused send names the adapter's fixed code (`ntfy delivery failed; notification remains pending:
   failed: publish: status_403`), never the address or answer; an address that is not an HTTPS topic is
   `...: refused_request: address: not_an_https_topic`, where Python described the expected form. The log's `logger` is `notifications-heartbeat`, the
   command's name, as every `swhurl-v2` command writes; Python's was `swhurl.notifications.heartbeat`.
8. **Bounds:** a pass stops after one minute. Without `--state` and without `HOME`, the command exits 1; Python fell
   back to the password database.

## All 13 heartbeat cases and the heartbeat's use of the notice

| Legacy case (`test_` prefix omitted) | Go coverage / scope |
| --- | --- |
| `HeartbeatActionTests.fresh_missing_suspended_and_old_checker` | `heartbeat.TestFreshMissingSuspendedAndOldChecker` |
| `HeartbeatActionTests.reminder_is_hourly_and_recovery_requires_a_prior_alert` | `heartbeat.TestReminderIsHourlyAndRecoveryRequiresAPriorAlert`; `orchestration.TestHeartbeatReminderKeepsTheIncidentStart` |
| `HeartbeatActionTests.freshness_boundary_future_success_and_invalid_timestamp` | `heartbeat.TestFreshnessBoundaryFutureSuccessAndInvalidTimestamp`; `heartbeat.TestUnreadableCronJobIsNoDecision` (difference 4) |
| `HeartbeatActionTests.transitions_leave_incoming_state_unchanged` | `heartbeat.TestTransitionsLeaveIncomingStateUnchanged` (a flag is a value, so the check is that the action and the next flag are right) |
| `HeartbeatActionTests.duration_parser` | `cmd/swhurl-v2.TestHeartbeatMaxAgeParser`; `cmd/swhurl-v2.TestNotificationsHeartbeatCLIFlags` |
| `HeartbeatCommandTests.dry_run_does_not_read_secret_post_or_save` | `orchestration.TestHeartbeatDryRunDoesNotReadTheSecretPostOrSave`; `cmd/swhurl-v2.TestNotificationsHeartbeatDryRunDoesNotReadSecretPostOrSave` |
| `HeartbeatCommandTests.stale_alert_persists_state_only_after_ntfy_accepts` | `orchestration.TestHeartbeatStaleAlertPersistsStateOnlyAfterNtfyAccepts`; `cmd/swhurl-v2.TestNotificationsHeartbeatAlertRecoverInPythonsWords`; `adapters/hoststate.TestReplaceIsPrivateAndLeavesNothingBehind` |
| `HeartbeatCommandTests.failed_cluster_read_leaves_existing_state_unchanged` | `orchestration.TestHeartbeatFailedClusterReadLeavesExistingStateUnchanged`; `cmd/swhurl-v2.TestNotificationsHeartbeatFailures/forbidden` |
| `HeartbeatCommandTests.missing_cronjob_alerts_and_secret_never_appears_in_errors` | `orchestration.TestHeartbeatMissingCronJobAlertsAndSecretNeverAppearsInErrors`; `cmd/swhurl-v2.TestNotificationsHeartbeatFailures` |
| `WatchedJobsTests.watched_jobs` | `heartbeat.TestWatchedJobs` |
| `WatchedJobsTests.each_job_uses_its_own_maximum_age_and_flag` | `orchestration.TestHeartbeatEachJobUsesItsOwnMaximumAgeAndFlag` |
| `WatchedJobsTests.one_unreadable_job_does_not_hide_the_other` | `orchestration.TestHeartbeatOneUnreadableJobDoesNotHideTheOther` |
| `WatchedJobsTests.state_written_before_there_were_other_jobs_still_reads` | `heartbeat.TestStateWrittenBeforeThereWereOtherJobsStillReads`; `heartbeat.TestInvalidStateIsRefusedNeverReset`; `orchestration.TestHeartbeatInvalidStateStopsBeforeAnyJob` |
| `test_notice.NoticeTests.priority_is_a_name_for_the_notify_service_and_a_number_for_ntfy` | The ntfy half: `heartbeat.TestNoticeWording`; `orchestration.TestHeartbeatStaleAlertPersistsStateOnlyAfterNtfyAccepts` (the exact document). The notify-service half is not the heartbeat's: the Go notify service owns that request |
| `test_notice.NoticeTests.an_outbox_entry_gives_the_same_notice_back` | Not the heartbeat's: outboxes belong to the Go checker and notify service |
| `test_notice.NoticeTests.ntfy_posts_the_notice_to_the_server_with_the_topic_in_the_body` | `adapters/ntfy.TestPublicationIsPostedToTheServerRoot`; `adapters/ntfy.TestContract` |
| `test_notice.NoticeTests.an_address_that_is_not_an_https_topic_is_refused_without_repeating_it` | `adapters/ntfy.TestContract`; `orchestration.TestHeartbeatMissingEmptyOrInvalidDestination` |
| `test_notice.NoticeTests.a_recorded_sender_keeps_or_refuses` | `adapters/ntfy/ntfytest.TestContract`; `adapters/ntfy/ntfytest.TestFakeCountsAndCanFail` (the fake replaces `Recorded`) |
| `test_notice.CheckerRetirementTests.python_checker_is_absent_but_shared_helpers_remain` | Not ported: it checks the Python package's layout, which the cutover deletes with this test |
| `test_notice.PlatformTopicTests.it_is_read_from_the_cluster_and_registered_as_a_secret` | `orchestration.TestHeartbeatStaleAlertPersistsStateOnlyAfterNtfyAccepts` (one named GET of `console/notification-ntfy`); every `cmd/swhurl-v2` heartbeat test checks no log line holds the address. Go never puts the address in a message, so it has nothing to redact |
| `test_notice.PlatformTopicTests.it_is_read_from_the_environment_where_the_secret_is_mounted` | Not the heartbeat's: the checker's pod path, covered by `cmd/swhurl-v2.TestCheckerExplicitEnvironmentAndFreshToken` |
| `test_notice.PlatformTopicTests.a_missing_empty_or_invalid_address_is_an_error_that_does_not_repeat_it` | `orchestration.TestHeartbeatMissingEmptyOrInvalidDestination` |

Beyond the legacy cases: `heartbeat.TestEncodingIsPythons` and `TestEncodedStateDecodes` (difference 1 and 2),
`orchestration.TestHeartbeatFreshJobDoesNothing`, `orchestration.TestHeartbeatSaveFailureIsAFailure`, the
`hoststate` contract (absent and unreadable kept apart, a failed replacement leaves the old bytes) and that adapter's own
tests (modes, no temporary file left after a replacement or a failed one, a file over 1 MiB), and
`cmd/swhurl-v2.TestNotificationsHeartbeatWithoutHome` (difference 8).
