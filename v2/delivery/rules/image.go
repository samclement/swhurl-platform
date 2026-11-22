package rules

import "github.com/samclement/swhurl-platform/v2/kernel"

// ImageMetadataOf accepts the declaration and full source revision an image states. The tag has
// already passed the platform's tag contract; its suffix must abbreviate the stated revision.
// Declaration validation precedes revision validation. Missing labels are delivery rule errors
// for the calling capability to translate; malformed labels retain the kernel's own errors.
func ImageMetadataOf(tag kernel.ImageTag, declarationLabel, revisionLabel string) (kernel.Declaration, string, error) {
	if declarationLabel == "" {
		return kernel.Declaration{}, "", invalid("declaration", "absent")
	}
	declaration, err := kernel.DecodeDeclaration([]byte(declarationLabel))
	if err != nil {
		return kernel.Declaration{}, "", err
	}
	if revisionLabel == "" {
		return kernel.Declaration{}, "", invalid("source", "revision_absent")
	}
	revision, err := kernel.RevisionOf(tag, revisionLabel)
	if err != nil {
		return kernel.Declaration{}, "", err
	}
	return declaration, revision, nil
}
