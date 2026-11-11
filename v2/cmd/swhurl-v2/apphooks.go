package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// The image webhook on every app repository whose staging release publishes the app's own image is one command: it
// reads each app's staging release record, lists the repository's webhooks and creates, repoints or leaves alone the
// one to Flux's app-images Receiver. The credentials are the environment's, never arguments: GH_TOKEN lists and
// writes the webhooks, and IMAGE_WEBHOOK_TOKEN, the Receiver's token, signs them.

// gitHubTokenEnv is the operator's GitHub token, as make verify-platform passes it too.
const gitHubTokenEnv = "GH_TOKEN"

// appHooks keeps the image webhooks and prints one line per app; an app that failed makes the exit status 1 after
// the others ran.
func (c cli) appHooks(r request) int {
	staging, status := c.stagingReleases(r.records)
	if status != 0 {
		return status
	}
	inputs := orchestration.AppHooksInputs{Staging: staging, Owner: r.owner, HookHost: r.hookHost, DryRun: r.dryRun,
		GitHubToken: strings.TrimSpace(c.environment(gitHubTokenEnv)), HookToken: strings.TrimSpace(c.environment(appWebhookTokenEnv)),
		Repos: c.appRepoWriter()}
	for _, missing := range []struct{ name, value string }{{gitHubTokenEnv, inputs.GitHubToken}, {appWebhookTokenEnv, inputs.HookToken}} {
		if missing.value == "" || missing.value == "REPLACE_ME" {
			return fail(c.diagnostic, errors.New("invalid: token: "+missing.name+" is not set"))
		}
	}
	outcomes, err := orchestration.AppHooks(context.Background(), inputs)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	failed := 0
	for _, outcome := range outcomes {
		if outcome.Problem != "" {
			fmt.Fprintf(c.diagnostic, "[ERROR] %s: %s\n", outcome.Application, outcome.Problem)
			failed++
			continue
		}
		fmt.Fprintf(c.out, "[OK] %s: image webhook on %s %s\n", outcome.Application, outcome.Repository,
			hookOutcome(outcome.State, r.dryRun))
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// hookOutcome is what happened to one repository's webhook, or in a dry run what would.
func hookOutcome(state kernel.ImageHookState, dryRun bool) string {
	done := map[kernel.ImageHookState]string{kernel.ImageHookMissing: "created", kernel.ImageHookStale: "updated"}[state]
	switch {
	case done == "":
		return "is in place"
	case dryRun:
		return "would be " + done
	}
	return done
}

// stagingReleases is each app's staging release record under the records tree, by app. An app with no staging
// release has none; a tree or a record that cannot be read stops the pass before any call, as a failed read is not
// absence.
func (c cli) stagingReleases(records string) (map[string][]byte, int) {
	entries, err := os.ReadDir(records)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: records: cannot read %s\n", records)
		return nil, 1
	}
	staging := map[string][]byte{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, status := c.checkoutFile(filepath.Join(records, entry.Name(), observe.StagingEnvironment, kernel.ReleaseRecordName))
		if status != 0 {
			return nil, status
		}
		if data != nil {
			staging[entry.Name()] = data
		}
	}
	return staging, 0
}

// registerAppHooksFlags adds what the image webhooks take: the records, the account the images are published
// under, the Receiver's host and the dry run.
func (r *request) registerAppHooksFlags(flags *flag.FlagSet) {
	flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
	flags.Var(&once{value: &r.owner}, "owner", "the account the apps' own images are published under (ghcr.io/OWNER/APP)")
	flags.Var(&once{value: &r.hookHost}, "hook-host", "the host Flux's app-images Receiver listens on")
	flags.Var(&onceBool{value: &r.dryRun}, "dry-run", "list and decide; write nothing to GitHub")
}
