package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/datadestroy"
	"github.com/samclement/swhurl-platform/v2/observe"
)

// destroyWait bounds each wait for a deleted object to go, as make destroy-data always has.
const destroyWait = 2 * time.Minute

// destroyPoll is the interval between reads while an object goes.
const destroyPoll = 2 * time.Second

// DestroyInputs is one destroy-data run: a target already parsed and confirmed, the cluster it reads and changes,
// and the clock its waits use.
type DestroyInputs struct {
	Reader  kube.Reader
	Writer  kube.Writer
	Secrets kube.SecretNamer
	Target  datadestroy.Target
	DryRun  bool
	Now     func() time.Time
	Sleep   func(time.Duration)
}

// DestroyOutcome is the volume the target's data lives on (empty for a claim that never bound) and the changes made.
type DestroyOutcome struct {
	Volume string
	Made   []datadestroy.Change
}

// DestroyData refuses while anything uses or would recreate the target, then makes the changes that delete it: the
// volume's reclaim policy, the claim, and the wait for the provisioner to delete the volume and its directory. A
// refusal is a *datadestroy.Refusal and changed nothing; a read that fails is an error, never an absence, so nothing
// is deleted on a guess. A dry run makes every check and no change.
func DestroyData(ctx context.Context, inputs DestroyInputs) (DestroyOutcome, error) {
	target := inputs.Target
	var outcome DestroyOutcome
	var err error
	if target.Kind == datadestroy.Claim {
		outcome.Volume, err = checkClaimReleased(ctx, inputs)
	} else {
		outcome.Volume = target.Name
		err = checkVolumeReleased(ctx, inputs)
	}
	if err != nil || inputs.DryRun {
		return outcome, err
	}
	for _, change := range datadestroy.Changes(target, outcome.Volume) {
		if err := makeChange(ctx, inputs, change, outcome.Volume); err != nil {
			return outcome, err
		}
		outcome.Made = append(outcome.Made, change)
	}
	return outcome, nil
}

// makeChange makes one change, and waits for what it deletes to go.
func makeChange(ctx context.Context, inputs DestroyInputs, change datadestroy.Change, name string) error {
	target := inputs.Target
	claim, _ := kube.RefOf("", "PersistentVolumeClaim", target.Namespace, target.Name)
	volume, _ := kube.RefOf("", "PersistentVolume", "", name)
	switch change {
	case datadestroy.SetReclaimDelete:
		if err := inputs.Writer.Patch(ctx, volume, []byte(datadestroy.ReclaimDelete)); err != nil {
			return fmt.Errorf("set PV %s's reclaim policy to Delete: %w", name, err)
		}
	case datadestroy.DeleteClaim:
		if err := inputs.Writer.Delete(ctx, claim); err != nil {
			return fmt.Errorf("delete PVC %s/%s: %w", target.Namespace, target.Name, err)
		}
		return awaitGone(ctx, inputs, claim, "PVC "+target.Namespace+"/"+target.Name)
	case datadestroy.AwaitVolumeGone:
		return awaitGone(ctx, inputs, volume, "PV "+name)
	}
	return nil
}

// checkClaimReleased refuses while a pod mounts the claim or a Helm release or Flux unit would recreate it, and
// returns the volume it is bound to.
func checkClaimReleased(ctx context.Context, inputs DestroyInputs) (string, error) {
	target := inputs.Target
	ref, _ := kube.RefOf("", "PersistentVolumeClaim", target.Namespace, target.Name)
	read, err := readObject(ctx, inputs.Reader, ref, "pvc")
	if err != nil {
		return "", err
	}
	if read == nil {
		return "", datadestroy.ClaimNotFound(target)
	}
	facts, err := datadestroy.ClaimFactsOf(read)
	if err != nil {
		return "", err
	}
	if err := checkNotMounted(ctx, inputs); err != nil {
		return "", err
	}
	if err := checkReleaseGone(ctx, inputs, facts.Release); err != nil {
		return "", err
	}
	return facts.Volume, checkUnitGone(ctx, inputs, facts)
}

// checkNotMounted refuses while an active pod in the claim's namespace mounts it.
func checkNotMounted(ctx context.Context, inputs DestroyInputs) error {
	podsRef, _ := kube.RefOf("", "Pod", inputs.Target.Namespace, "")
	pods, err := inputs.Reader.ListNamespace(ctx, podsRef)
	if err != nil {
		return err
	}
	if pods.Status != observe.Present {
		return fmt.Errorf("failed: pods: %s", pods.Reason)
	}
	mounted, err := datadestroy.MountedByActivePod(pods.Items, inputs.Target.Name)
	if err != nil {
		return err
	}
	if mounted {
		return datadestroy.ClaimMounted(inputs.Target)
	}
	return nil
}

// checkReleaseGone refuses while Helm still records the release that installed the claim.
func checkReleaseGone(ctx context.Context, inputs DestroyInputs, release string) error {
	if release == "" {
		return nil
	}
	names, err := inputs.Secrets.SecretNames(ctx, inputs.Target.Namespace, datadestroy.HelmSelector(release))
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return datadestroy.ReleaseExists(inputs.Target, release)
	}
	return nil
}

// checkUnitGone refuses while the Flux Kustomization that applied the claim exists.
func checkUnitGone(ctx context.Context, inputs DestroyInputs, facts datadestroy.ClaimFacts) error {
	if facts.Unit == "" {
		return nil
	}
	unit, err := kube.RefOf("kustomize.toolkit.fluxcd.io", "Kustomization", facts.UnitNamespace, facts.Unit)
	if err != nil {
		return err
	}
	read, err := readObject(ctx, inputs.Reader, unit, "kustomization")
	if err != nil {
		return err
	}
	if read != nil {
		return datadestroy.UnitExists(facts.Unit)
	}
	return nil
}

// checkVolumeReleased refuses a volume that is missing or still bound.
func checkVolumeReleased(ctx context.Context, inputs DestroyInputs) error {
	ref, _ := kube.RefOf("", "PersistentVolume", "", inputs.Target.Name)
	read, err := readObject(ctx, inputs.Reader, ref, "pv")
	if err != nil {
		return err
	}
	if read == nil {
		return datadestroy.VolumeNotFound(inputs.Target)
	}
	return datadestroy.VolumeReleased(inputs.Target, read)
}

// readObject reads one object: its document, nil when the server says it is absent, or an error when the read failed.
func readObject(ctx context.Context, reader kube.Reader, ref observe.Ref, subject string) ([]byte, error) {
	read, err := reader.Read(ctx, ref)
	if err != nil {
		return nil, err
	}
	switch read.Status {
	case observe.Present:
		return read.Object, nil
	case observe.Absent:
		return nil, nil
	}
	return nil, fmt.Errorf("failed: %s: %s", subject, read.Reason)
}

// awaitGone polls until the server says the object is absent. A read that fails is retried until the wait is over,
// and is named when it times out.
func awaitGone(ctx context.Context, inputs DestroyInputs, ref observe.Ref, what string) error {
	started := inputs.Now()
	last := ""
	for {
		read, err := inputs.Reader.Read(ctx, ref)
		switch {
		case err != nil:
			last = err.Error()
		case read.Status == observe.Absent:
			return nil
		case read.Status != observe.Present:
			last = read.Reason
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s: wait stopped: %w", what, ctx.Err())
		}
		if inputs.Now().Sub(started) >= destroyWait {
			if last != "" {
				return fmt.Errorf("%s still present after %s (last read: %s)", what, destroyWait, last)
			}
			return fmt.Errorf("%s still present after %s", what, destroyWait)
		}
		inputs.Sleep(destroyPoll)
	}
}
