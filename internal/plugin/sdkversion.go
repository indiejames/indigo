package plugin

import (
	"fmt"
	"strconv"
	"strings"
)

// sdkVersion is a parsed MAJOR.MINOR.PATCH. Prerelease and build suffixes are
// accepted but ignored: compatibility is decided by the release line alone.
type sdkVersion struct {
	major, minor, patch int
}

func (v sdkVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func parseSDKVersion(s string) (sdkVersion, error) {
	core := strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return sdkVersion{}, fmt.Errorf("%q is not a MAJOR.MINOR.PATCH version", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return sdkVersion{}, fmt.Errorf("%q is not a MAJOR.MINOR.PATCH version", s)
		}
		n[i] = v
	}
	return sdkVersion{n[0], n[1], n[2]}, nil
}

// checkSDKVersion decides whether a plugin built against SDK version declared
// can run on an editor built against SDK version supported (sdk.Version).
//
// It follows semver as the SDK promises it: the major versions must match,
// and while they are 0 the minor versions must too, since every 0.x minor
// bump may break the API. From 1.0 on, an editor runs plugins built against
// any minor version up to its own; a plugin built against a *newer* minor may
// call methods this editor does not implement, so it is refused with a message
// saying the editor is what needs upgrading. Patch versions never matter.
//
// A manifest with no sdk_version at all is accepted, with a warning: plugins
// written before the check existed omit it, and refusing them would break
// working setups over a field nobody was told was required. A value that is
// present but malformed is a definite mistake and is refused.
func checkSDKVersion(declared, supported string) (warning string, err error) {
	if strings.TrimSpace(declared) == "" {
		return fmt.Sprintf("declares no sdk_version; loading it without a compatibility check (this indigo provides SDK %s)", supported), nil
	}
	want, err := parseSDKVersion(declared)
	if err != nil {
		return "", fmt.Errorf("invalid sdk_version: %w", err)
	}
	have, err := parseSDKVersion(supported)
	if err != nil {
		// sdk.Version is a constant in this repository; this is a build mistake.
		return "", fmt.Errorf("editor's own SDK version is invalid: %w", err)
	}

	switch {
	case want.major != have.major:
		return "", fmt.Errorf("built against SDK %s, but this indigo provides SDK %s (major versions differ); %s",
			want, have, rebuildOrUpgrade(want, have))
	case have.major == 0 && want.minor != have.minor:
		return "", fmt.Errorf("built against SDK %s, but this indigo provides SDK %s (0.x minor versions are not compatible); %s",
			want, have, rebuildOrUpgrade(want, have))
	case want.minor > have.minor:
		return "", fmt.Errorf("built against SDK %s, newer than the SDK %s this indigo provides; upgrade indigo",
			want, have)
	}
	return "", nil
}

// rebuildOrUpgrade names which side is behind, so the message says what to do
// rather than only that something is wrong.
func rebuildOrUpgrade(want, have sdkVersion) string {
	if want.major > have.major || (want.major == have.major && want.minor > have.minor) {
		return "upgrade indigo"
	}
	return "rebuild the plugin against SDK " + fmt.Sprintf("%d.%d", have.major, have.minor)
}
