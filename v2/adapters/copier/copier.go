// Package copier renders one stack template: one pinned Copier subprocess and nothing else. Copier is the fourth
// approved subprocess (see the `no-subprocess` rule and docs/iteration-2-plan.md#guardrails); it clones a public
// template anonymously and writes the answers into a destination the caller owns. No credential reaches its
// environment or its arguments, and it runs under a fixed deadline.
//
// The template's own questions are read from its copier.yml here too, so the file's shape has one reader: what the
// template accepts is Copier's, not the platform's.
package copier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	// RefusedRender is the code every failure here carries: a template this adapter could not render.
	RefusedRender = "refused_render"
	// MaxOutput bounds what one run may report back; Copier's output is a progress log, not a document.
	MaxOutput = 64 << 10
	// RenderTimeout bounds one render: a template clone and its file writes.
	RenderTimeout = 5 * time.Minute
	// waitDelay bounds what a killed process's children may keep writing after it.
	waitDelay = 10 * time.Second
	// maxDetail bounds how much of Copier's own message a refusal carries.
	maxDetail = 200
)

// Request is one render: the template at one revision, rendered with these answers into Dest. The caller includes
// every answer the template asks for, app_name among them.
type Request struct {
	Template string // the template's URL, or a local directory a test renders
	Revision string // the commit v2/contract/stacks.json pins
	Answers  map[string]string
	Dest     string // the directory to render into; it must be empty or not exist
}

// Renderer renders one stack template.
type Renderer interface {
	Render(ctx context.Context, request Request) error
}

// Error names a fixed reason and, as Detail, at most one line of what Copier said. No credential reaches the
// subprocess, so its message holds nothing this adapter hid.
type Error struct{ Reason, Detail string }

func (e *Error) Error() string {
	if e.Detail == "" {
		return RefusedRender + ": " + e.Reason
	}
	return RefusedRender + ": " + e.Reason + ": " + e.Detail
}

func refuse(reason string) error { return &Error{Reason: reason} }

// Command runs Copier from PATH, or the pinned specifier through uvx when Copier is not installed.
type Command struct {
	Binary    string
	Uvx       string
	Specifier string // the pinned Copier, as docs/iteration-2-plan.md#guardrails approves it
	Timeout   time.Duration
}

// New is the adapter with the version v2/contract/stacks.json pins: the image installs it, and an operator's machine
// that has uvx runs the same version.
func New(specifier string) *Command {
	return &Command{Binary: "copier", Uvx: "uvx", Specifier: specifier, Timeout: RenderTimeout}
}

var _ Renderer = (*Command)(nil)

func (c *Command) Render(ctx context.Context, request Request) error {
	if err := validate(request); err != nil {
		return err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = RenderTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	program := c.program()
	command := exec.CommandContext(ctx, program[0], append(program[1:], arguments(request)...)...)
	command.Env = environment()
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	command.WaitDelay = waitDelay
	err := command.Run()
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Reason: "timeout", Detail: fmt.Sprintf("no render within %s", timeout)}
	}
	return &Error{Reason: "copier_failed", Detail: detail(output.Bytes())}
}

// validate refuses a request before a process starts: a render names a template, a revision and a destination.
func validate(request Request) error {
	if strings.TrimSpace(request.Template) == "" || strings.TrimSpace(request.Revision) == "" || request.Dest == "" {
		return refuse("incomplete_request")
	}
	for name, value := range request.Answers {
		if !answerName(name) || strings.ContainsAny(value, "\r\n\x00") {
			return refuse("invalid_answer")
		}
	}
	return nil
}

// answerName is a template question's name: a Copier data key, never an option or a path.
var answerNames = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func answerName(name string) bool { return answerNames.MatchString(name) }

// program is Copier when it is installed, and the pinned one through uvx when it is not.
func (c *Command) program() []string {
	binary := c.Binary
	if binary == "" {
		binary = "copier"
	}
	if _, err := exec.LookPath(binary); err == nil {
		return []string{binary}
	}
	uvx := c.Uvx
	if uvx == "" {
		uvx = "uvx"
	}
	return []string{uvx, c.Specifier}
}

// arguments is one render: every answer in a fixed order, so the same request runs the same command.
func arguments(request Request) []string {
	args := []string{"copy", "--defaults", "--quiet", "--vcs-ref", request.Revision}
	for _, name := range slices.Sorted(maps.Keys(request.Answers)) {
		args = append(args, "--data", name+"="+request.Answers[name])
	}
	return append(args, request.Template, request.Dest)
}

// environment is what Copier and the Git it runs may see: no token, no kubeconfig, nothing from this process that
// could name a credential. The template is public, so the render needs no identity at all.
func environment() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "XDG_CACHE_HOME"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// detail is the last line Copier wrote, bounded, for a person: its progress lines name files, not secrets.
func detail(output []byte) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	last := strings.Join(strings.Fields(lines[len(lines)-1]), " ")
	if len(last) > maxDetail {
		return last[:maxDetail] + "…"
	}
	return last
}

// Question is one of a stack template's choice questions (copier.yml), offered as a feature.
type Question struct {
	Name    string
	Help    string
	Choices []string
	Default string
}

// fixedQuestions are asked by the platform itself (the name and description fields), never offered as features.
var fixedQuestions = []string{"app_name", "description"}

// Questions reads a copier.yml's choice questions, in file order. A question without choices asks for free text, so
// it is not one this platform offers; the template's private keys (`_...`) are not questions either.
func Questions(document []byte) ([]Question, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(document, &root); err != nil || len(root.Content) == 0 {
		return nil, refuse("questions_unreadable")
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, refuse("questions_unreadable")
	}
	found := []Question{}
	for i := 0; i+1 < len(top.Content); i += 2 {
		name, spec := top.Content[i].Value, top.Content[i+1]
		if strings.HasPrefix(name, "_") || slices.Contains(fixedQuestions, name) || spec.Kind != yaml.MappingNode {
			continue
		}
		choices, ok := choicesOf(spec)
		if !ok {
			continue
		}
		found = append(found, Question{Name: name, Help: scalarOf(spec, "help"), Choices: choices,
			Default: defaultOf(spec, choices)})
	}
	return found, nil
}

// choicesOf is a question's choices, in the file's order: a list of values, or a mapping whose values are them.
func choicesOf(spec *yaml.Node) ([]string, bool) {
	node := memberOf(spec, "choices")
	if node == nil || (node.Kind != yaml.SequenceNode && node.Kind != yaml.MappingNode) {
		return nil, false
	}
	choices := []string{}
	for i, item := range node.Content {
		if node.Kind == yaml.MappingNode && i%2 == 0 {
			continue // a mapping's keys name the choices; its values are them
		}
		choices = append(choices, strings.TrimSpace(item.Value))
	}
	return choices, len(choices) > 0
}

// defaultOf is the answer the template starts from: its default, or the first choice when it names none.
func defaultOf(spec *yaml.Node, choices []string) string {
	if value := scalarOf(spec, "default"); value != "" {
		return value
	}
	return choices[0]
}

// memberOf is one key of a mapping node, or nil.
func memberOf(spec *yaml.Node, name string) *yaml.Node {
	for i := 0; i+1 < len(spec.Content); i += 2 {
		if spec.Content[i].Value == name {
			return spec.Content[i+1]
		}
	}
	return nil
}

// scalarOf is one scalar member of a mapping node, or "".
func scalarOf(spec *yaml.Node, name string) string {
	if node := memberOf(spec, name); node != nil && node.Kind == yaml.ScalarNode {
		return node.Value
	}
	return ""
}
