package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// team: one change to the team registry, and the notification files rendered from it.

const (
	teamNote = "the team registry and the notification files rendered from it; nothing is applied to the cluster"
	// maxAddress bounds the one line a channel's address is read from.
	maxAddress = 1024
)

// teamActions is every action the command takes and the flags each may be given beside it.
var teamActions = map[kernel.TeamAction]struct{ description, rename, transport bool }{
	kernel.AddTeam:      {description: true},
	kernel.DescribeTeam: {description: true},
	kernel.RenameTeam:   {rename: true},
	kernel.RemoveTeam:   {},
	kernel.GiveChannel:  {transport: true},
	kernel.DropChannel:  {},
}

// teamDocument is the report: the team, what was done, the channel it was about and every file written or removed.
type teamDocument struct {
	Kind    string          `json:"kind"`
	Version int             `json:"version"`
	Team    string          `json:"team"`
	Action  string          `json:"action"`
	Channel string          `json:"channel,omitempty"`
	Files   []files.Outcome `json:"files"`
	Note    string          `json:"note"`
}

// registerTeamFlags adds what a team change names: the records tree, the team, the action and its one value.
func (r *request) registerTeamFlags(flags *flag.FlagSet) {
	flags.Var(&once{value: &r.records}, "records", "the tree every v2-owned instance keeps its records in")
	flags.Var(&once{value: &r.team}, "team", "the team the change is about")
	flags.Var(&once{value: &r.action}, "action", "add, describe, rename, remove, channel or drop-channel")
	r.descriptionFlag = &once{value: &r.description}
	flags.Var(r.descriptionFlag, "description", "one line saying what it owns")
	flags.Var(&once{value: &r.rename}, "rename", "the team's new name")
	flags.Var(&once{value: &r.transport}, "transport", "the channel's transport (default ntfy)")
	flags.Var(&once{value: &r.profile}, "profile", "platform profile file")
	r.registerOut(flags)
}

// teamNamed is what a team change requires, and refuses a flag its action does not take.
func (r *request) teamNamed() []string {
	takes, known := teamActions[kernel.TeamAction(r.action)]
	if !known || (r.descriptionFlag.set && !takes.description) || (r.rename != "" && !takes.rename) ||
		(r.transport != "" && !takes.transport) || (takes.rename && r.rename == "") {
		return []string{""}
	}
	return []string{r.records, r.team, r.action, r.profile, r.out}
}

// team applies one change to the registry. A channel's address is read from standard input, never from an argument,
// so it is in no process listing or shell history; it is never printed.
func (c cli) team(r request) int {
	if c.appTreeRefused(r.out) {
		return 2
	}
	address := ""
	if kernel.TeamAction(r.action) == kernel.GiveChannel {
		var err error
		if address, err = readAddress(c.stdin); err != nil {
			return fail(c.diagnostic, err)
		}
	}
	outcome, err := orchestration.ChangeRegistryIn(r.checkout(), kernel.TeamChange{Action: kernel.TeamAction(r.action),
		Team: r.team, Description: r.description, Rename: r.rename, Transport: r.transport}, address, c.writer,
		c.encrypter)
	if err != nil {
		return c.wrote(outcome.Written, err)
	}
	fmt.Fprintln(c.diagnostic, outcome.Summary())
	return c.emitTeam(outcome)
}

// readAddress is the first line of standard input, without its line ending.
func readAddress(stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", errors.New("invalid: address: expected the channel's address on standard input")
	}
	line, err := bufio.NewReaderSize(io.LimitReader(stdin, maxAddress+1), maxAddress+1).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("io: address: cannot read standard input")
	}
	if len(line) > maxAddress {
		return "", errors.New("invalid: address: longer than 1024 bytes")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("invalid: address: expected the channel's address on standard input")
	}
	return line, nil
}

// emitTeam writes the report to stdout.
func (c cli) emitTeam(outcome orchestration.RegistryOutcome) int {
	document := teamDocument{Kind: "v2-team", Version: 1, Team: outcome.Team, Action: string(outcome.Action),
		Channel: outcome.Channel, Files: nonNilOutcomes(outcome.Written), Note: teamNote}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, bytes.TrimSuffix(out.Bytes(), []byte{'\n'}))
}
