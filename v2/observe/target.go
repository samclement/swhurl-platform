package observe

// TargetOf derives the target an instance's compiled HelmRelease describes: its objects, selector and
// intended artifact. It recognises the shape the compiler renders and refuses anything else, so a
// chart change that moved a name or a label fails here rather than reading another workload.
//
// Two facts are not in the document and stay the caller's: which unit owns the instance, and which
// source revision Flux is expected to have applied.
func TargetOf(helmRelease []byte, unit Ref, revision string) (Target, error) {
	document, err := releaseDocument(helmRelease)
	if err != nil {
		return Target{}, err
	}
	target := Target{
		Namespace: document.Metadata.Namespace,
		Unit:      unit,
		Release:   Ref{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Namespace: document.Metadata.Namespace, Name: document.Metadata.Name},
		// The chart names a single-controller workload after the release, not after the controller.
		Workload: Ref{APIVersion: "apps/v1", Kind: "Deployment", Namespace: document.Metadata.Namespace, Name: document.Metadata.Name},
		Pods:     Ref{APIVersion: "v1", Kind: "Pod", Namespace: document.Metadata.Namespace},
		// The chart labels every pod of a controller with the release's instance and controller name.
		Selector:  "app.kubernetes.io/instance=" + document.Metadata.Name + ",app.kubernetes.io/controller=main",
		Container: "main",
		Digest:    document.digest(),
		Replicas:  document.replicas(),
		Revision:  revision,
	}
	if err := target.validate(); err != nil {
		return Target{}, err
	}
	return target, nil
}

// UnitRef reads the identity of a compiled Flux unit document.
func UnitRef(unit []byte) (Ref, error) {
	var document struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err := decodeDocument(unit, &document, "unit"); err != nil {
		return Ref{}, err
	}
	return Ref{APIVersion: document.APIVersion, Kind: document.Kind,
		Namespace: document.Metadata.Namespace, Name: document.Metadata.Name}, nil
}

// release is the part of a compiled HelmRelease the target needs. The chart's own conventions give the
// workload its name and labels; this is the shape the compiler writes, nothing more.
type release struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Values struct {
			Controllers map[string]struct {
				Type       string `json:"type"`
				Replicas   *int32 `json:"replicas"`
				Containers map[string]struct {
					Image struct {
						Digest string `json:"digest"`
					} `json:"image"`
				} `json:"containers"`
			} `json:"controllers"`
		} `json:"values"`
	} `json:"spec"`
}

// releaseDocument decodes the HelmRelease and refuses a shape this slice does not recognise: more than
// one controller, a controller that is not a Deployment, or a missing container, digest or replica
// count. Guessing any of them would describe a workload the instance does not have.
func releaseDocument(helmRelease []byte) (release, error) {
	var document release
	if err := decodeDocument(helmRelease, &document, "release"); err != nil {
		return release{}, err
	}
	if document.Metadata.Name == "" || document.Metadata.Namespace == "" {
		return release{}, invalid("release", "metadata_required")
	}
	if err := document.checkController(); err != nil {
		return release{}, err
	}
	return document, nil
}

// checkController refuses a controller shape the target cannot describe.
func (r release) checkController() error {
	if len(r.Spec.Values.Controllers) != 1 {
		return invalid("release", "single_controller_required")
	}
	controller, found := r.Spec.Values.Controllers["main"]
	if !found {
		return invalid("release", "single_controller_required")
	}
	if controller.Type != "" && controller.Type != "deployment" {
		return invalid("release", "controller_type_unsupported")
	}
	if _, found := controller.Containers["main"]; !found {
		return invalid("release", "container_required")
	}
	if !digestPattern().MatchString(r.digest()) {
		return invalid("release", "digest_required")
	}
	if !r.hasReplicas() || r.replicas() < 0 {
		return invalid("release", "replicas_required")
	}
	return nil
}

// digest is the intended artifact the chart pins.
func (r release) digest() string {
	return r.Spec.Values.Controllers["main"].Containers["main"].Image.Digest
}

// hasReplicas reports whether the document states an intended replica count, which is a value even
// when it is zero.
func (r release) hasReplicas() bool { return r.Spec.Values.Controllers["main"].Replicas != nil }

// replicas is the intended count; releaseDocument establishes that it is present.
func (r release) replicas() int32 {
	if declared := r.Spec.Values.Controllers["main"].Replicas; declared != nil {
		return *declared
	}
	return 0
}
