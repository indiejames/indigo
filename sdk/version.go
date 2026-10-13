package sdk

// Version is this SDK's semantic version. It is bumped together with the
// sdk/vX.Y.Z tag that publishes it, never separately.
//
// The editor is built against the SDK in its own repository, so this is also
// the version a given indigo binary supports: a plugin's manifest declares the
// version it was built against (sdk_version in plugin.toml) and the editor
// compares the two before starting it. While the major version is 0, a minor
// bump is a breaking change; from 1.0 on, only a major bump is.
const Version = "0.1.0"
