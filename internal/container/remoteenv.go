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
// and covers a CLI that does not. An unset variable with no default becomes
// empty, which is what the spec and VS Code do. Any other ${...} is left as it
// is.
//
// A nil value — the spec's "unset this variable" — is dropped: `docker exec
// -e` can set a variable but not remove one the image defines, so honouring it
// is not possible here, and passing an empty value instead would be a
// different instruction from the one the file gave.
//
// Sorted by name, so the resulting command line is stable.
func ResolveRemoteEnv(remoteEnv map[string]*string, containerEnv map[string]string, lookupLocal func(string) (string, bool)) []string {
	keys := make([]string, 0, len(remoteEnv))
	for k, v := range remoteEnv {
		if v != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
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
			if !ok || v == "" {
				return def
			}
			return v
		})
		out = append(out, k+"="+val)
	}
	return out
}

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
