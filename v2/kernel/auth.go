package kernel

import "slices"

// The platform's verification facts live in one object per managed namespace: a ConfigMap cannot be read
// across namespaces, so centralising them means one rendered source fanning out to one object per namespace,
// which is what RenderAuth builds. The object's name is the same everywhere, so an instance's pod mounts a
// fixed name and a rotation changes this folder alone.
const (
	authObjectName = "swhurl-auth"
	authFolder     = "auth"
	// authOwner is the platform, not an instance: the objects are this renderer's, and an instance claiming
	// one would be a second owner.
	authOwner = "platform"
)

// RenderAuth renders the auth material for every managed namespace: one ConfigMap per namespace with the
// platform's issuer and keys, and the kustomization that lists them. It refuses a profile that states no keys
// and a set of namespaces with nothing in it, because an empty unit is a mistake rather than a state.
func RenderAuth(namespaces []string, profile PlatformProfile) (ResourceSet, error) {
	if !profile.Auth.configured() {
		return ResourceSet{}, refuse(Unsupported, "auth", "the profile states no signing keys")
	}
	managed := make([]string, 0, len(namespaces))
	for _, namespace := range slices.Sorted(slices.Values(namespaces)) {
		if namespace == "" || slices.Contains(managed, namespace) {
			continue
		}
		if !labelPattern.MatchString(namespace) {
			return ResourceSet{}, refuse(Invalid, "auth.namespace", "expected a DNS label")
		}
		managed = append(managed, namespace)
	}
	if len(managed) == 0 {
		return ResourceSet{}, refuse(Unsupported, "auth", "the snapshot names no instance to render for")
	}
	files := make([]File, 0, len(managed)+1)
	listed := make([]any, 0, len(managed))
	claims := make([]Claim, 0, len(managed))
	for _, namespace := range managed {
		name := AuthFileName(namespace)
		files = append(files, File{authFolder + "/" + name, encodeManifest(authConfigMapFor(namespace, profile.Auth))})
		listed = append(listed, name)
		claims = append(claims, Claim{Object{"", "ConfigMap", namespace, authObjectName}, authOwner})
	}
	files = append(files, File{authFolder + "/kustomization.yaml", encodeManifest(manifest{
		"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": listed})})
	return ResourceSet{Files: files, Inventory: claims}, nil
}

// authConfigMapFor is the platform's verification facts in one namespace. It is the same document in every
// namespace: only the namespace differs.
func authConfigMapFor(namespace string, auth Auth) manifest {
	return manifest{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": metadata(authObjectName, namespace),
		"data":     manifest{"SWHURL_AUTH_ISSUER": auth.Issuer, "SWHURL_AUTH_KEYS": auth.Keys}}
}

// AuthFileName is one namespace's file in the platform's auth folder: the objects share a name, so the file carries
// the namespace that tells them apart. A removal deletes the file and the registration that lists it, so the name is
// stated once here rather than derived in a second place.
func AuthFileName(namespace string) string { return authObjectName + "-" + namespace + ".json" }
