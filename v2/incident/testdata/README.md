# Notification checker fixtures and legacy-case map

This is a test-fixture contract, not a progress log. The checker runs only Go; the Python implementation
and tests are retired. Their frozen captures and the case map below remain the retained-behavior evidence. See
[current notification behavior](../../../docs/services.md#current-notification-behavior).

## Provenance and boundaries

- `legacy.json`: **138 captured Evaluate calls** (two synthetic pending notices normalized below), captured before Go implementation by running the **40 retained tests**
  from [the retired Python tests](https://github.com/samclement/swhurl-platform/blob/25dbe522a87eeec16791b383c8c34a7a6ca6930a/tests/test_notifications.py), with inputs and expected output deep-copied at each call. Capture ran against
  the unchanged Python implementation; 40 tests passed. The two `JobTests` were deliberately excluded. Go's
  `TestLegacyEvaluations` checks every captured result and verifies input/previous-state ownership. Nothing regenerates
  the golden answers during a check; the Python implementation is evidence, not a runtime Go dependency. The capture
  held 139 calls: the one of `LifecycleTests.app_template_fixtures_use_the_same_discovery` went with the retired
  `tests/fixtures/apps` root it checked.
- `snapshot.json`: the healthy app snapshot from that capture, without Events. Integrations add API-required name/namespace
  fields the pure-domain fixture does not need. No Secret or real address appears in these inputs.
- `state-v1.json`: a sanitized **real-shaped Python-produced** version-1 incident/outbox document taken from the frozen
  failure-retry scenario, with synthetic notify-service incident and unknown nested fields added for round-trip tests.
  It is **not a live-cluster state export**. Times, release UID and failure key agree with the scenario. The unknown large
  integer proves unknown values do not round through float64. Actual live-document decoding and scheduled continuation
  were verified separately at [cutover](../../../docs/current-state.md#notification-checker-scheduled-in-go-7-november-2026).

Domain tests are under `v2/incident`; effect sequencing/failure/restart tests under `v2/orchestration`; command/environment
and scheduled Git wiring tests under `v2/cmd/swhurl-v2`. The Kubernetes and notify-service adapters run shared HTTP/fake
contracts over raw fixtures. ntfy's existing shared contract remains unchanged. Go tests need no Python process. A one-off offline differential check also fed all 139 Go-encoded results back through
Python's actual `read_state` and next-pass `evaluate`; every resumed result agreed, and Python read/saved the unknown-field
fixture without losing its exact large integer. Integral timestamps are written as JSON floats so Python keeps the same
spelling in time-based notice keys after a Go checkpoint.

## Synthetic pending-notice adaptations

The production decoder requires persisted notices to carry a nonempty string `key`, string `title` and `message`,
finite numeric `priority` and `queued`; optional known notice/incident/instance fields must have the right types.
Unknown extension fields remain untouched. Missing `queued` cannot become a zero-time retry expiry, and a string
`notified` cannot become false and clear incident history.

Only these two artificial pending notices in `legacy.json` were made structurally valid, in **both** the `previous`
input and matching `expected` output. Existing fields and the behavior those cases exercise are unchanged:

| Case (zero-based capture index) | Original synthetic notice | Fields added to that notice |
| --- | --- | --- |
| `LifecycleTests.test_evaluation_leaves_nested_previous_state_and_snapshot_unchanged` (80) | `{"key":"pending","about":["example","staging"]}` | `title: "Synthetic pending notice"`, `message: ""`, `priority: 3`, `queued: 1791028800.0` (the previous state's start) |
| `LifecycleTests.test_seen_expiry_is_strict_and_does_not_discard_pending_notices` (135) | `{"key":"pending","queued":1791021599.0}` | `title: "Synthetic pending notice"`, `message: ""`, `priority: 3`; original expired `queued` retained |

No other captured input or expected output changed during fixture preparation; the Python source/tests were unchanged
at capture time and removed only after live acceptance. Decoder refusal tests cover
missing delivery/retry fields and mistyped known fields; orchestration tests prove refusal happens after only the
named state GET, before any snapshot read, save or send. These deliberately corrupt states are no longer allowed
to borrow the permissive conventions of an ownership/expiry test fixture.

## Intentional differences

1. Core Events are field-filtered by involved kind; only Deployments and StatefulSets are read. Existing kube APIs retain
   all their previous functionality; CronJob/DaemonSet reads are simply not part of this use case.
2. The two scheduled-job test cases are outside the approved model; v2 does not support job instances.
3. A refused app copy is requeued **before any later checkpoint**, not held in a process-local waiting list; retries
   survive successful later sends, a later checkpoint failure, and restart. One refusal skips the other copies for the
   pass without holding up the platform topic. Copy failures alone do not fail the pass; platform/state/snapshot failures do.
4. The standalone Go command requires explicit `CONSOLE_URL`; it does not parse checkout YAML or hardcode a cluster domain.
5. Current `./v2-apps/instances/<app>/<env>` paths are recognized alongside the legacy `./apps` root, fixing Python discovery.
   A resumed state may therefore acquire app identities it never recorded before. Existing `started` and Event-history
   boundaries still apply; only a truly absent state establishes a new baseline. Live state continuation was verified at cutover.

The named state GET is an API/RBAC correction, **not expanded permissions**: a field-selected ConfigMap LIST still needs
`list`, whereas the existing Role grants only `get`/`patch` on `notification-state`. Pending copies, notice keys, wording,
incident clocks, state version, wire documents and at-least-once acceptance/checkpoint semantics are retained.

## All 41 legacy cases

`golden` means `incident.TestLegacyEvaluations/<class>.<case>` (including each captured subscenario). Tests named additionally
cover effects or diagnostics not represented by an Evaluate result. Two rows are explicit scope omissions, not successes.

| Legacy case (`test_` prefix omitted) | Go coverage / scope |
| --- | --- |
| `JobTests.a_failed_run_is_one_notice_at_once_and_the_next_success_recovers_it` | Omitted: scheduled-job path explicitly dropped |
| `JobTests.a_running_waiting_or_suspended_job_is_not_an_incident` | Omitted: scheduled-job path explicitly dropped |
| `LifecycleTests.evaluation_leaves_nested_previous_state_and_snapshot_unchanged` | golden |
| `LifecycleTests.recreated_release_resets_old_incident_and_action_history` | golden |
| `LifecycleTests.seen_expiry_is_strict_and_does_not_discard_pending_notices` | golden |
| `LifecycleTests.removed_identity_is_pruned_only_after_history_and_uninstall` | golden |
| `LifecycleTests.deployment_waits_for_ready_even_if_helm_wait_was_disabled` | golden |
| `LifecycleTests.deployment_is_announced_while_the_unit_waits_on_its_dependencies` | golden |
| `LifecycleTests.a_held_deployment_logs_why` | golden; `incident.TestHeldDeploymentDiagnostic` |
| `LifecycleTests.scale_to_zero_does_not_claim_an_image_was_deployed` | golden |
| `LifecycleTests.event_input_order_is_sorted_before_failure_and_rollback_updates` | golden |
| `LifecycleTests.failure_attempts_are_deduplicated_immediately_and_history_keeps_last_sixteen` | golden |
| `LifecycleTests.event_time_fallback_and_history_boundary_with_source_controller` | golden |
| `LifecycleTests.same_pass_upgrade_failure_and_rollback_are_combined` | golden |
| `LifecycleTests.baseline_does_not_replay_history_and_noop_is_silent` | golden |
| `LifecycleTests.lifecycle_titles_environments_and_priorities` | golden |
| `LifecycleTests.filters_tests_pushes_other_controllers_unmanaged_and_old_uids` | golden |
| `LifecycleTests.uninstall_survives_unit_namespace_and_event_removal_and_keeps_storage_context` | golden |
| `LifecycleTests.a_unit_failing_while_its_instance_is_removed_is_not_a_failed_deployment` | golden |
| `LifecycleTests.removal_does_not_announce_while_release_finalizer_is_pending` | golden |
| `LifecycleTests.failure_retries_are_grouped_and_not_duplicated_by_health` | golden |
| `IncidentTests.grace_hourly_reminder_and_two_minute_recovery` | golden |
| `IncidentTests.console_allowance_and_flapping` | golden |
| `IncidentTests.stopped_suspended_and_blocked` | golden |
| `IncidentTests.old_healthy_image_stalls_and_git_only_revision_does_not_reset_clock` | golden |
| `IncidentTests.completed_rollback_recovers_availability_without_claiming_requested_image_deployed` | golden |
| `CopyTests.an_apps_notice_has_one_copy_for_the_notify_service_and_the_consoles_has_none` | golden |
| `CopyTests.a_copy_that_is_not_accepted_never_holds_up_the_platform_topic_and_is_given_up_after_the_retry_window` | golden; `orchestration.TestCheckerRetriesSurviveEveryCheckpointAndLaterFailure` |
| `CopyTests.the_operator_hears_when_the_notify_service_stops_accepting_and_when_it_does_again` | golden; `orchestration.TestCheckerCopyServiceFailureAndImmediateRecovery` |
| `CopyTests.one_refusal_ends_the_pass_for_the_other_copies` | golden; `orchestration.TestCheckerRetriesSurviveEveryCheckpointAndLaterFailure` |
| `CopyTests.forwarding_uses_the_default_notify_token_path_without_an_override` | `cmd/swhurl-v2.TestCheckerExplicitEnvironmentAndFreshToken` |
| `CopyTests.forwarding_names_the_app_and_proves_who_sends_with_the_token_file` | `adapters/notifysvc.TestContract; cmd/swhurl-v2.TestCheckerExplicitEnvironmentAndFreshToken; orchestration.TestCheckerSavedBeforeSendingAndAfterEachAcceptance` |
| `DeliveryTests.outbox_saved_before_send_and_checkpointed_after_acceptance` | golden; `orchestration.TestCheckerSavedBeforeSendingAndAfterEachAcceptance` |
| `DeliveryTests.send_failure_keeps_pending_for_next_run_and_never_acknowledges` | golden; `orchestration.TestCheckerPlatformFailureAndRestart` |
| `DeliveryTests.failed_checkpoint_stops_before_network` | golden; `orchestration.TestCheckerCheckpointFailureStopsNetwork` |
| `DeliveryTests.dry_run_has_no_posts_or_state_mutations` | golden; `orchestration.TestCheckerDryRunAndUnknownSnapshot; cmd/swhurl-v2.TestNotificationsCheckDryRunBothCredentials` |
| `DeliveryTests.unknown_snapshot_does_not_recover_and_monitor_alerts_after_five_minutes` | golden; `orchestration.TestCheckerDryRunAndUnknownSnapshot` |
| `DeliveryTests.corrupt_and_oversize_state_refuse_silent_reset` | `incident.TestInvalidStateNeverResets; incident.TestStateSizeBoundUsesPythonASCIIEncoding` |
| `DeliveryTests.http_errors_do_not_expose_destination_or_response` | `adapters/ntfy.TestContract; adapters/notifysvc.TestContract` |
| `WiringTests.state_not_owned_by_flux_and_account_has_only_named_state_writes` | `cmd/swhurl-v2.TestCheckerStateRBACAndSourceAlertWiring; adapters/kube.TestCheckerCollectionContract` |
| `WiringTests.source_alert_does_not_duplicate_app_console_or_pin_notifications` | `cmd/swhurl-v2.TestCheckerStateRBACAndSourceAlertWiring; cmd/swhurl-v2.TestCheckerEveryFluxUnitHasTheExpectedAlertOwnership` |
