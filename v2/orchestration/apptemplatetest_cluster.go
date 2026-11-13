package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/apptemplatetest"
	"github.com/samclement/swhurl-platform/v2/lifecycletest"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// The objects the app-template test reads and changes, each a fixed kind the adapter states.
func appTemplateUnitRef(app string) observe.Ref {
	ref, _ := kube.WritableRefOf(fluxUnitGroup, "Kustomization", apptemplatetest.FluxNamespace, apptemplatetest.Unit(app))
	return ref
}

func appTemplateNamespaceRef(app string) observe.Ref {
	ref, _ := kube.RefOf("", "Namespace", "", apptemplatetest.Namespace(app))
	return ref
}

func appTemplateServicesRef() observe.Ref {
	ref, _ := kube.RefOf("", "Service", apptemplatetest.Namespace(apptemplatetest.Worker), "")
	return ref
}

func appTemplateIngressesRef() observe.Ref {
	ref, _ := kube.RefOf("networking.k8s.io", "Ingress", apptemplatetest.Namespace(apptemplatetest.Worker), "")
	return ref
}

func appTemplateDeploymentRef() observe.Ref {
	ref, _ := kube.RefOf("apps", "Deployment", apptemplatetest.Namespace(apptemplatetest.Web), apptemplatetest.Workload)
	return ref
}

func appTemplatePodsRef() observe.Ref {
	ref, _ := kube.RefOf("", "Pod", apptemplatetest.Namespace(apptemplatetest.Web), "")
	return ref
}

func appTemplateClaimRef() observe.Ref {
	ref, _ := kube.RefOf("", "PersistentVolumeClaim", apptemplatetest.Namespace(apptemplatetest.Web), apptemplatetest.Claim)
	return ref
}

func appTemplateReaderRef() observe.Ref {
	ref, _ := kube.WritableRefOf("", "Pod", apptemplatetest.Namespace(apptemplatetest.Web), apptemplatetest.ReaderPod)
	return ref
}

func appTemplateVolumeRef(name string) observe.Ref {
	ref, _ := kube.RefOf("", "PersistentVolume", "", name)
	return ref
}

// list is a namespace's objects of one kind, those the selector names when there is one; a list the server did not
// answer is an error, never an empty one.
func (r *appTemplateRun) list(ref observe.Ref, selector string) ([][]byte, error) {
	var found observe.List
	var err error
	if selector == "" {
		found, err = r.inputs.Cluster.Reader.ListNamespace(r.ctx, ref)
	} else {
		found, err = r.inputs.Cluster.Reader.List(r.ctx, ref, selector)
	}
	if err != nil {
		return nil, err
	}
	if found.Status != observe.Present {
		return nil, fmt.Errorf("failed: %s in %s: %s", ref.Kind, ref.Namespace, found.Reason)
	}
	return found.Items, nil
}

// written tests the database through a short-lived pod that mounts the web app's claim, and says what it found.
func (r *appTemplateRun) written() error {
	ref := appTemplateReaderRef()
	what := fmt.Sprintf("pod %s/%s", ref.Namespace, ref.Name)
	if r.inputs.Cluster.Writer.Delete(r.ctx, ref) == nil {
		_ = r.wait(appTemplateDeleted, what+" still present", r.gone(ref))
	}
	if err := r.inputs.Cluster.Writer.Create(r.ctx, ref, apptemplatetest.ReaderDocument()); err != nil {
		return fmt.Errorf("create %s: %w", what, err)
	}
	phase := ""
	finished := func() (bool, error) {
		document, err := r.read(ref)
		if err != nil || document == nil {
			return false, err
		}
		phase, err = lifecycletest.PhaseOf(document)
		return apptemplatetest.Finished(phase), err
	}
	if err := r.wait(appTemplateReader, what+" not finished", finished); err != nil {
		return err
	}
	r.say(apptemplatetest.Written(phase))
	if err := r.inputs.Cluster.Writer.Delete(r.ctx, ref); err != nil {
		return fmt.Errorf("delete %s: %w", what, err)
	}
	return r.wait(appTemplateDeleted, what+" still present", r.gone(ref))
}

// cleanup deletes both units, each namespace while it carries render's label naming its app, and every volume the
// web app's claim was seen bound to whose claim lived in its namespace. Nothing it does fails the run, and it runs
// even when the caller's context is cancelled.
func (r *appTemplateRun) cleanup() {
	c := &appTemplateRun{liveCluster: r.liveCluster, inputs: r.inputs}
	c.ctx = context.WithoutCancel(r.ctx)
	for _, app := range apptemplatetest.Apps() {
		c.deleteGone(appTemplateUnitRef(app), appTemplateDeleted)
	}
	if claim, err := c.read(appTemplateClaimRef()); err == nil && claim != nil {
		if volume, err := lifecycletest.VolumeOf(claim); err == nil {
			r.remember(volume)
		}
	}
	for _, app := range apptemplatetest.Apps() {
		namespace := appTemplateNamespaceRef(app)
		if document, err := c.read(namespace); err == nil && document != nil && apptemplatetest.Labelled(document, app) {
			c.deleteGone(namespace, appTemplateNamespace)
		}
	}
	for _, volume := range r.volumes {
		c.destroyVolume(volume)
	}
}

// deleteGone deletes one object and waits up to limit for it to go.
func (r *appTemplateRun) deleteGone(ref observe.Ref, limit time.Duration) {
	if r.inputs.Cluster.Writer.Delete(r.ctx, ref) == nil {
		_ = r.wait(limit, "", r.gone(ref))
	}
}

// destroyVolume lets the provisioner delete one volume, only when its claim lived in the web app's namespace.
func (r *appTemplateRun) destroyVolume(name string) {
	ref := appTemplateVolumeRef(name)
	volume, err := r.read(ref)
	if err != nil || volume == nil || !apptemplatetest.Claimed(volume) {
		return
	}
	_ = r.inputs.Cluster.Writer.Patch(r.ctx, ref, []byte(apptemplatetest.ReclaimDelete))
	_ = r.wait(appTemplateVolume, "", r.gone(ref))
}

// remember adds a volume the cleanup destroys, once.
func (r *appTemplateRun) remember(volume string) {
	for _, known := range r.volumes {
		if known == volume {
			return
		}
	}
	if volume != "" {
		r.volumes = append(r.volumes, volume)
	}
}
