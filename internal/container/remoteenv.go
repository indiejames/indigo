package container

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// envVarRef matches the two devcontainer.json variable forms that name an
// environment: ${containerEnv:NAME} and ${localEnv:NAME}, each with an
// optional ":default".
var envVarRef = regexp.MustCompile(`\$\{(containerEnv|localEnv):([A-Za-z_][A-Za-z0-9_]*)(?::([^}]*))?\}`)

// ResolveRemoteEnv turns devcontainer.json's remoteEnv into "K=V" entries for
// the server's environment.
//
// ${containerEnv:NAME} is resolved against containerEnv — the container's own
// environment — and ${localEnv:NAME} against lookupLocal. The canonical use is
//
//	"remoteEnv": {"PATH": "${containerEnv:PATH}:/home/vscode/.local/bin"}
//
// which has to be resolved against the container: the CLI's read-configuration
// has no container to ask and leaves the reference in place. localEnv is
// normally substituted by the CLI already; resolving it here too is harmless
// and covers a CLI that does not. The default applies only when the variable
// is absent — one that is present but empty stays empty — and an absent one
// with no default becomes empty. That matches the devcontainer CLI's own
// substitution (checked against 0.89.0's source: a string value is used as is,
// otherwise the default, otherwise ""). Any other ${...} is left as it is.
//
// A nil value is the spec's "unset this variable". `docker exec -e` cannot
// remove a variable the image defines, so the names travel in UnsetEnvVar
// instead, as the last entry, and indigo-server — the process that exec
// starts — removes them from its own environment before starting anything,
// so the server and the tools it runs do not inherit the image's value.
//
// Sorted by name, so the result is stable.
func ResolveRemoteEnv(remoteEnv map[string]*string, containerEnv map[string]string, lookupLocal func(string) (string, bool)) []string {
	keys := make([]string, 0, len(remoteEnv))
	var unset []string
	for k, v := range remoteEnv {
		if v != nil {
			keys = append(keys, k)
		} else {
			unset = append(unset, k)
		}
	}
	sort.Strings(keys)
	sort.Strings(unset)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		val := envVarRef.ReplaceAllStringFunc(*remoteEnv[k], func(ref string) string {
			m := envVarRef.FindStringSubmatch(ref)
			kind, name, def := m[1], m[2], m[3]
			var v string
			var ok bool
			if kind == "containerEnv" {
				v, ok = containerEnv[name]
			} else if lookupLocal != nil {
				v, ok = lookupLocal(name)
			}
			if !ok {
				return def
			}
			return v
		})
		out = append(out, k+"="+val)
	}
	if len(unset) > 0 {
		out = append(out, UnsetEnvVar+"="+strings.Join(unset, ","))
	}
	return out
}

// UnsetEnvVar carries remoteEnv's null entries — comma-separated variable
// names — to indigo-server, which unsets them and then this variable itself
// at startup. Names are identifiers, so a comma cannot occur in one.
const UnsetEnvVar = "INDIGO_UNSET_ENV"

// WithoutContainerEnvRefs drops the entries that refer to ${containerEnv:...},
// for when the container's environment could not be read. Resolving them
// anyway would give their defaults — usually empty — and the common
// "${containerEnv:PATH}:/extra" would then *replace* PATH with "/extra",
// breaking every tool on it. Dropping them leaves the image's own value.
func WithoutContainerEnvRefs(remoteEnv map[string]*string) map[string]*string {
	kept := make(map[string]*string, len(remoteEnv))
	for k, v := range remoteEnv {
		if v == nil || !strings.Contains(*v, "${containerEnv:") {
			kept[k] = v
		}
	}
	return kept
}

// InspectEnv returns the container's environment as the runtime records it:
// the image's ENV plus devcontainer.json's containerEnv, which is exactly what
// ${containerEnv:...} refers to. Read with inspect rather than by running `env`
// inside, so it needs no shell or tools in the image and is not coloured by
// whatever a login shell would add.
func (d Docker) InspectEnv(ctx context.Context, id string) (map[string]string, error) {
	out, err := d.output(ctx, "inspect", "--format", "{{json .Config.Env}}", id)
	if err != nil {
		return nil, err
	}
	var list []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &list); err != nil {
		return nil, fmt.Errorf("parse container env: %w", err)
	}
	env := make(map[string]string, len(list))
	for _, kv := range list {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env, nil
}
