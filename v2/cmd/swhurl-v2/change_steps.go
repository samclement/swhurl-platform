package main

import (
	"context"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/apps"
	"github.com/samclement/swhurl-platform/v2/console"
	"github.com/samclement/swhurl-platform/v2/delivery"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/provisioning"
)

// The steps a kind's change runs in its working copy: console's table turns the change's flags into typed steps, and
// each one runs here as one call to the capability that owns it, with the effects only the command holds.

// request is what the change's flags ask for, as console's steps read it.
func (s *changeSpec) request() console.Request {
	return console.Request{App: s.app, Env: s.env, Team: s.team, Settings: s.settings, Calls: s.calls,
		Subscriptions: s.subscriptions, DropCalls: s.dropCalls, DropSubscriptions: s.dropSubscriptions,
		Allows: s.allows, Disallows: s.disallows, Description: s.description, Rename: s.rename,
		Transport: s.transport, Image: s.image}
}

// runChangeStep runs one step on the working copy and prints its one line. A refusal is v2's own words, on the
// diagnostic, not a paraphrase.
func (c cli) runChangeStep(step console.Step, tree files.Checkout, revision string) int {
	summary, err := c.runStep(step, tree, revision)
	if err != nil {
		return c.failure(err)
	}
	fmt.Fprintln(c.out, summary)
	return 0
}

// runStep is one step as the call its capability owns, and the line it reports.
func (c cli) runStep(step console.Step, tree files.Checkout, revision string) (string, error) {
	switch step := step.(type) {
	case console.CreateStep:
		return c.runCreate(step, tree)
	case console.EditStep:
		setting, err := apps.ParseSetting(step.Setting)
		if err != nil {
			return "", err
		}
		if setting.AppLevel() {
			result, err := apps.SetTeamIn(tree, step.App, setting.Team, c.writer)
			return result.Summary(), err
		}
		result, err := apps.EditIn(tree, step.App, step.Env, setting, c.writer)
		return result.Summary(), err
	case console.RemoveStep:
		result, err := apps.RemoveIn(tree, step.App, step.Env, c.writer)
		return result.Summary(), err
	case console.PromoteStep:
		result, err := delivery.PromoteIn(tree, step.App, step.Env, kernel.InstanceRevision(step.Expected), c.writer)
		return result.Summary(), err
	case console.RenderStep:
		result, err := provisioning.Render(tree, revision, c.writer)
		return result.Summary(), err
	case console.TeamStep:
		return c.runTeam(step, tree)
	}
	return "", fmt.Errorf("invalid: step: %s has no call", step.Operation())
}

// runCreate states a new app's records with a seed drawn for its broker identity.
func (c cli) runCreate(step console.CreateStep, tree files.Checkout) (string, error) {
	seed, err := c.seed()
	if err != nil {
		return "", &files.Error{Code: files.IO, Path: "randomness", Reason: "cannot read a seed"}
	}
	result, err := apps.CreateIn(context.Background(), tree, apps.CreateRequest{Application: step.App,
		Team: step.Team, Image: step.Image, Exposure: step.Exposure, Seed: seed}, c.registries, c.writer, c.encrypter)
	return result.Summary(), err
}

// runTeam changes the registry; a channel's address comes from standard input, as the team command reads it.
func (c cli) runTeam(step console.TeamStep, tree files.Checkout) (string, error) {
	address := ""
	if step.Change.Action == kernel.GiveChannel {
		var err error
		if address, err = readAddress(c.stdin); err != nil {
			return "", err
		}
	}
	result, err := orchestration.ChangeRegistryIn(tree, step.Change, address, c.writer, c.encrypter)
	return result.Summary(), err
}
