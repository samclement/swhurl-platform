package observe

// RouteTarget is the route an instance's records expect: the Ingress the compiler renders, and the certificate
// each of its TLS secrets needs. cert-manager names the Certificate it issues for an Ingress after the secret,
// so each is read by that name; an Ingress the records do not expect, a solver's own included, is not read.
type RouteTarget struct {
	Ingress      Ref
	Certificates []Ref
}

// Route is what the cluster says about the route the records expect: whether its Ingress is there, and the hosts
// it serves when it is.
type Route struct {
	Name    string
	Outcome string // present, absent, unavailable or not_assessed
	Reason  string
	Hosts   []string
}

// Certificate is whether one certificate the route needs is ready.
type Certificate struct {
	Name    string
	Outcome string // ready, not_ready, absent, unavailable or not_assessed
	Reason  string
}

// Problem is one container that is not ready and says why: the waiting or terminated reason its status carries,
// and the start of its message.
type Problem struct {
	Pod, Container, Reason, Message string
}

// RouteTargetOf derives the route a compiled Ingress describes. It refuses a document that is not one, rather
// than reading another object as the instance's route.
func RouteTargetOf(ingress []byte) (RouteTarget, error) {
	var document struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			TLS []struct {
				SecretName string `json:"secretName"`
			} `json:"tls"`
		} `json:"spec"`
	}
	if err := decodeDocument(ingress, &document, "route"); err != nil {
		return RouteTarget{}, err
	}
	if document.Kind != "Ingress" || document.APIVersion != "networking.k8s.io/v1" {
		return RouteTarget{}, invalid("route", "expected_an_ingress")
	}
	namespace := document.Metadata.Namespace
	route := RouteTarget{
		Ingress:      Ref{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: namespace, Name: document.Metadata.Name},
		Certificates: []Ref{},
	}
	for _, tls := range document.Spec.TLS {
		route.Certificates = append(route.Certificates,
			Ref{APIVersion: "cert-manager.io/v1", Kind: "Certificate", Namespace: namespace, Name: tls.SecretName})
	}
	return route, nil
}

// owns reports whether the route asked for this object.
func (r RouteTarget) owns(ref Ref) bool {
	if ref == r.Ingress {
		return true
	}
	for _, certificate := range r.Certificates {
		if ref == certificate {
			return true
		}
	}
	return false
}

// roles is every object the route reads, each in the instance's namespace.
func (r RouteTarget) roles() []role {
	roles := []role{{"route", r.Ingress, true, true}}
	for _, certificate := range r.Certificates {
		roles = append(roles, role{"route.certificates", certificate, true, true})
	}
	return roles
}

// routesOf answers the route the records expect and each certificate it needs. An instance the records give no
// route answers neither: an empty list is the record's own fact, not a gap.
func routesOf(target Target, reads map[Ref]Read) ([]Route, []Certificate, error) {
	routes, certificates := []Route{}, []Certificate{}
	if target.Route == nil {
		return routes, certificates, nil
	}
	route, err := routeOf(readFor(reads, target.Route.Ingress))
	if err != nil {
		return nil, nil, err
	}
	routes = append(routes, route)
	for _, ref := range target.Route.Certificates {
		certificate, err := certificateOf(readFor(reads, ref))
		if err != nil {
			return nil, nil, err
		}
		certificates = append(certificates, certificate)
	}
	return routes, certificates, nil
}

// routeOf is one Ingress read: whether it is there, and the hosts its rules serve.
func routeOf(read Read) (Route, error) {
	presence := presenceFinding("route", read)
	route := Route{Name: read.Ref.Name, Outcome: presence.Outcome, Reason: presence.Reason, Hosts: []string{}}
	if read.Status != Present {
		return route, nil
	}
	var document struct {
		Spec struct {
			Rules []struct {
				Host string `json:"host"`
			} `json:"rules"`
		} `json:"spec"`
	}
	if err := decodeDocument(read.Object, &document, "route"); err != nil {
		return Route{}, err
	}
	for _, rule := range document.Spec.Rules {
		if rule.Host != "" {
			route.Hosts = append(route.Hosts, rule.Host)
		}
	}
	return route, nil
}

// certificateOf is one Certificate read: its Ready condition, about the generation it observed.
func certificateOf(read Read) (Certificate, error) {
	if read.Status != Present {
		presence := presenceFinding("certificate", read)
		return Certificate{Name: read.Ref.Name, Outcome: presence.Outcome, Reason: presence.Reason}, nil
	}
	var document struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Status struct {
			Conditions []condition `json:"conditions"`
		} `json:"status"`
	}
	if err := decodeDocument(read.Object, &document, "certificate"); err != nil {
		return Certificate{}, err
	}
	if document.Metadata.Generation < 1 {
		return Certificate{}, invalid("certificate", "missing_generation")
	}
	ready := fluxReadiness("certificate", document.Status.Conditions, document.Metadata.Generation, Ready, NotReady, ReasonNotTrue)
	return Certificate{Name: read.Ref.Name, Outcome: ready.Outcome, Reason: ready.Reason}, nil
}
