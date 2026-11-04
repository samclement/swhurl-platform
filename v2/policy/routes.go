package policy

import (
	"encoding/json"
	"slices"
	"strings"
)

type ingress struct {
	IngressClassName string
	Rules            []struct {
		Host string
		HTTP struct {
			Paths []struct {
				Backend struct {
					Service struct {
						Name string
						Port struct{ Number int64 }
					}
				}
			}
		}
	}
	TLS []struct {
		Hosts      []string
		SecretName string
	}
}

func (c *check) checkRoutes() {
	count := 0
	for _, one := range c.objects {
		if one.Kind != "Ingress" || !c.inNamespace(one.Metadata) {
			continue
		}
		count++
		c.checkIngress(one)
	}
	if c.facts.exposure == "authenticated-web" && count != 1 {
		c.add("exposure", "instance", "signed-in exposure needs exactly one route")
	}
	if c.facts.exposure == "private" && count != 0 {
		c.add("exposure", "instance", "private instances must have no route")
	}
}
func (c *check) checkIngress(one object) {
	var route ingress
	if json.Unmarshal(one.Spec, &route) != nil {
		c.add("exposure", one.ref(), "invalid Ingress specification")
		return
	}
	middleware := one.Metadata.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]
	domain := c.facts.platform.CookieDomain
	for _, rule := range route.Rules {
		c.checkHost(one, route, rule.Host, middleware, domain)
	}
	c.checkCompiledRoute(one)
	if len(route.Rules) == 0 {
		c.add("exposure", one.ref(), "route must name a host")
	}
}
func (c *check) checkHost(one object, route ingress, host, middleware, domain string) {
	inDomain := host == domain || strings.HasSuffix(host, "."+domain)
	if inDomain && !slices.Contains(strings.Split(middleware, ","), c.facts.platform.Middleware) {
		c.add("exposure", one.ref(), "routes under the cookie domain must use sign-in middleware")
	}
	if c.facts.exposure == "authenticated-web" && !inDomain {
		c.add("exposure", one.ref(), "signed-in routes must stay inside the profile domain")
	}
	c.checkTLS(one, route, host)
}
func (c *check) checkCompiledRoute(one object) {
	for _, expected := range c.facts.source {
		if expected.Kind != "Ingress" {
			continue
		}
		if !sameJSON(one.Spec, expected.Spec) {
			c.add("exposure", one.ref(), "route differs from the compiled host, TLS or service")
		}
		for _, key := range []string{"cert-manager.io/cluster-issuer", "traefik.ingress.kubernetes.io/router.middlewares"} {
			if one.Metadata.Annotations[key] != expected.Metadata.Annotations[key] {
				c.add("exposure", one.ref(), "route annotations differ from profile routing")
			}
		}
	}
}

func (c *check) checkTLS(one object, route ingress, host string) {
	covered := false
	for _, tls := range route.TLS {
		if tls.SecretName != "" && slices.Contains(tls.Hosts, host) {
			covered = true
		}
	}
	if !covered {
		c.add("ingress-tls", one.ref(), "every host must have a TLS certificate reference")
	}
}
func (c *check) checkEvents() {
	expected := eventObjects(c.facts.source)
	actual := eventObjects(c.objects)
	if !sameJSON(expected, actual) {
		c.add("events", "instance", "streams and consumers must exactly match the compiled resolved events")
	}
}
func eventObjects(objects []object) map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	for _, one := range objects {
		if one.Kind == "Stream" || one.Kind == "Consumer" {
			result[one.ref()+"/"+one.Metadata.Namespace] = one.Spec
		}
	}
	return result
}
