package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// The image webhook makes the first scan of an app's ImageRepository sooner: GitHub calls Flux's app-images Receiver
// when the app's repository publishes a package, instead of the platform waiting for ImageInterval. These are the
// decisions about it — which apps have one, where it posts and whether a repository's webhooks need a change — with
// the token and every GitHub call left to the caller.

const (
	// imageHookReceiver and imageHookNamespace name the Flux Receiver the webhook calls; Flux derives its path from
	// them and the token, which is why the platform builds that path rather than storing it.
	imageHookReceiver  = "app-images"
	imageHookNamespace = "flux-system"
	// imageHookRegistry is where an app repository's workflow publishes its own image.
	imageHookRegistry = "ghcr.io"
)

// ImageHookEvents are the GitHub events an image publication sends: a package, by either name GitHub uses. The
// list is the caller's own.
func ImageHookEvents() []string { return []string{"package", "registry_package"} }

// ImageHookURL is where Flux's app-images Receiver on host listens for a webhook signed with token.
func ImageHookURL(token, host string) string {
	digest := sha256.Sum256([]byte(token + imageHookReceiver + imageHookNamespace))
	return "https://" + host + "/hook/" + hex.EncodeToString(digest[:])
}

// ImageHookApp is one app the image webhook is kept for: the repository its staging release states, or the problem
// that keeps its record from saying.
type ImageHookApp struct{ Application, Repository, Problem string }

// ImageHookApps is every app, in name order, whose staging release (by application) publishes the app's own
// platform image, ghcr.io/<owner>/<app>: the image its repository's workflow pushes, so the package events reach the
// app's ImageRepository. An app publishing another image has no webhook and is left out; a record that cannot be
// decoded, or that names another application, is a problem rather than an app left out, since its image is unknown.
func ImageHookApps(staging map[string][]byte, owner string) []ImageHookApp {
	names := make([]string, 0, len(staging))
	for name := range staging {
		names = append(names, name)
	}
	sort.Strings(names)
	found := []ImageHookApp{}
	for _, name := range names {
		release, err := DecodeRelease(staging[name])
		switch {
		case err != nil:
			found = append(found, ImageHookApp{Application: name, Problem: "the staging release record cannot be read: " + err.Error()})
		case release.Application != name:
			found = append(found, ImageHookApp{Application: name, Problem: "the staging release record names " + release.Application})
		case release.Artifact.Image == imageHookRegistry+"/"+strings.ToLower(owner)+"/"+name:
			found = append(found, ImageHookApp{Application: name, Repository: release.Source.Repository})
		}
	}
	return found
}

// ListedHook is one webhook as a repository lists it.
type ListedHook struct {
	ID     int
	Active bool
	Events []string
	URL    string
}

// ImageHookState is what a repository's image webhook needs.
type ImageHookState string

const (
	// ImageHookInPlace is a webhook that posts to the expected URL, active and with exactly the image events.
	ImageHookInPlace ImageHookState = "in-place"
	// ImageHookMissing is a repository with no webhook to the Receiver's host: one is created.
	ImageHookMissing ImageHookState = "missing"
	// ImageHookStale is a webhook to the Receiver's host that posts elsewhere (the token was rotated), is disabled
	// or takes other events: that webhook is changed.
	ImageHookStale ImageHookState = "stale"
)

// ImageHookPlan is the change one repository's image webhook needs; ID is the stale webhook to change.
type ImageHookPlan struct {
	State ImageHookState
	ID    int
}

// PlanImageHook decides from a repository's webhooks what keeps its image webhook posting to want, the URL
// ImageHookURL names. A webhook belongs to the platform when it posts to want's host. Any correct one is enough and
// is left alone; otherwise the first of the platform's is changed, and with none of them one is created. Webhooks to
// other hosts are never the platform's, and nothing is ever deleted: a second webhook to the host stays as it is.
func PlanImageHook(listed []ListedHook, want string) ImageHookPlan {
	host := hookHost(want)
	stale := 0
	for _, hook := range listed {
		if host == "" || !strings.EqualFold(hookHost(hook.URL), host) {
			continue
		}
		if hook.Active && hook.URL == want && sameEvents(hook.Events) {
			return ImageHookPlan{State: ImageHookInPlace}
		}
		if stale == 0 {
			stale = hook.ID
		}
	}
	if stale != 0 {
		return ImageHookPlan{State: ImageHookStale, ID: stale}
	}
	return ImageHookPlan{State: ImageHookMissing}
}

// hookHost is the host a webhook URL posts to, or "" for one that is not a URL.
func hookHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// sameEvents is whether a webhook takes exactly the image events, in any order.
func sameEvents(events []string) bool {
	sorted := slices.Clone(events)
	slices.Sort(sorted)
	return slices.Equal(sorted, ImageHookEvents())
}
