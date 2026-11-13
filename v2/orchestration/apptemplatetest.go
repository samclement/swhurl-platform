package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/route"
	"github.com/samclement/swhurl-platform/v2/apptemplatetest"
	"github.com/samclement/swhurl-platform/v2/lifecycletest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The app-template test's waits: make live-test-app-template's own bounds, and the lifecycle test's for what it
// left unbounded.
const (
	appTemplateReady     = 10 * time.Minute
	appTemplateTries     = 30
	appTemplateBetween   = 5 * time.Second
	appTemplateReader    = 2 * time.Minute
	appTemplateNamespace = 3 * time.Minute
	appTemplateVolume    = 2 * time.Minute
	appTemplateDeleted   = 5 * time.Minute
)

// AppTemplateTestInputs is the cluster the test changes, the route it asks, what it takes from the fixture's render,
// the clock its waits use, and where each line goes as it is decided.
type AppTemplateTestInputs struct {
	Cluster  Cluster
	Route    route.Prober
	Rendered apptemplatetest.Rendered
	Now      func() time.Time
	Sleep    func(time.Duration)
	Say      func(apptemplatetest.Line)
}

// AppTemplateTest deploys the fixture's two rendered instances through their own units, checks them, and removes
// them. Leftovers from an earlier run, or a cluster it cannot read, are a *apptemplatetest.Preflight and nothing is
// changed. A unit that is not Ready, or a read that fails, is one failed line and the checks after it do not run; a
// failed check is a failed line and the test goes on. The passed line follows when nothing failed, and the cleanup
// always runs last, even when the caller's context is cancelled. The lines are returned as well as said.
func AppTemplateTest(ctx context.Context, inputs AppTemplateTestInputs) ([]apptemplatetest.Line, error) {
	run := &appTemplateRun{liveCluster: liveCluster{ctx, inputs.Cluster.Reader, inputs.Now, inputs.Sleep}, inputs: inputs}
	if err := run.preflight(); err != nil {
		return nil, err
	}
	for _, step := range []func() error{run.install, run.checks} {
		if err := step(); err != nil {
			run.say(apptemplatetest.Failed(err.Error()))
			break
		}
	}
	for _, line := range apptemplatetest.Conclusion(run.lines) {
		run.say(line)
	}
	run.cleanup()
	run.say(apptemplatetest.CleanedUp())
	return run.lines, nil
}

// appTemplateRun is one run: the lines said so far, and every volume the web app's claim was seen bound to, which
// the cleanup destroys.
type appTemplateRun struct {
	liveCluster
	inputs  AppTemplateTestInputs
	lines   []apptemplatetest.Line
	volumes []string
}

func (r *appTemplateRun) say(line apptemplatetest.Line) {
	r.lines = append(r.lines, line)
	if r.inputs.Say != nil {
		r.inputs.Say(line)
	}
}

// preflight refuses when either app's namespace or unit already exists, or when it cannot tell.
func (r *appTemplateRun) preflight() error {
	for _, app := range apptemplatetest.Apps() {
		for _, ref := range []observe.Ref{appTemplateNamespaceRef(app), appTemplateUnitRef(app)} {
			present, err := r.present(ref)
			if err != nil {
				return &apptemplatetest.Preflight{Reason: "cannot check for leftovers: " + err.Error()}
			}
			if present {
				return apptemplatetest.Leftovers(app)
			}
		}
	}
	return nil
}

// install creates both units from the render, then waits for each to be Ready, in turn.
func (r *appTemplateRun) install() error {
	for _, app := range apptemplatetest.Apps() {
		if err := r.inputs.Cluster.Writer.Create(r.ctx, appTemplateUnitRef(app), r.inputs.Rendered.Units[app]); err != nil {
			return fmt.Errorf("create Kustomization %s: %w", apptemplatetest.Unit(app), err)
		}
	}
	for _, app := range apptemplatetest.Apps() {
		message := ""
		ready := func() (bool, error) {
			document, err := r.read(appTemplateUnitRef(app))
			if err != nil || document == nil {
				return false, err
			}
			message = apptemplatetest.ReadyMessage(document)
			return lifecycletest.Ready(document)
		}
		if err := r.wait(appTemplateReady, apptemplatetest.Namespace(app)+" unit not Ready", ready); err != nil {
			if message != "" {
				return fmt.Errorf("%w: %s", err, message)
			}
			return err
		}
		r.say(apptemplatetest.UnitReady(app))
	}
	return nil
}

// checks reads each instance and says each verdict, in the order make live-test-app-template has always said them.
func (r *appTemplateRun) checks() error {
	if err := r.unexposed(); err != nil {
		return err
	}
	deployment, err := r.read(appTemplateDeploymentRef())
	if err != nil {
		return err
	}
	security, pinned := apptemplatetest.Deployed(deployment)
	r.say(security)
	r.protected()
	if err := r.claim(); err != nil {
		return err
	}
	if err := r.written(); err != nil {
		return err
	}
	for _, line := range pinned {
		r.say(line)
	}
	pods, err := r.list(appTemplatePodsRef(), apptemplatetest.PodSelector)
	if err != nil {
		return err
	}
	r.say(apptemplatetest.ProbedReady(pods, r.inputs.Rendered.HealthPath))
	return nil
}

// unexposed says whether the worker's namespace holds any Service or Ingress.
func (r *appTemplateRun) unexposed() error {
	services, err := r.list(appTemplateServicesRef(), "")
	if err != nil {
		return err
	}
	ingresses, err := r.list(appTemplateIngressesRef(), "")
	if err != nil {
		return err
	}
	r.say(apptemplatetest.Unexposed(len(services), len(ingresses)))
	return nil
}

// protected asks the web app's route until it answers the sign-in redirect, 30 times 5 seconds apart, and says the
// last answer when it never did.
func (r *appTemplateRun) protected() {
	host := r.inputs.Rendered.Host
	last := ""
	for try := 1; ; try++ {
		answer, err := r.inputs.Route.Get(r.ctx, "https://"+host+"/")
		if err == nil && apptemplatetest.SignInRedirect(answer.Status, answer.Location) {
			r.say(apptemplatetest.Protected(host, true, ""))
			return
		}
		last = apptemplatetest.Answer(answer.Status, answer.Location)
		if err != nil {
			last = err.Error()
		}
		if try == appTemplateTries || r.ctx.Err() != nil {
			r.say(apptemplatetest.Protected(host, false, last))
			return
		}
		r.sleep(appTemplateBetween)
	}
}

// claim says whether the web app's claim is Bound on the retained class, and remembers its volume for the cleanup.
func (r *appTemplateRun) claim() error {
	claim, err := r.read(appTemplateClaimRef())
	if err != nil {
		return err
	}
	if claim != nil {
		volume, err := lifecycletest.VolumeOf(claim)
		if err != nil {
			return err
		}
		r.remember(volume)
	}
	r.say(apptemplatetest.ClaimBound(claim))
	return nil
}
