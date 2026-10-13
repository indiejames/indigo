# Releasing

Releases are built by `.github/workflows/release.yml`. Push a tag and it does the rest:

```sh
git tag 2026.10.0
git push origin 2026.10.0
```

Editor releases use **calendar versions**: `YYYY.M.N`, the year, the month without a leading
zero, and a counter that starts at 0 each month (`2026.10.0`, `2026.10.1`, then `2026.11.0`).
There is **no leading `v`**. Go reads a `v`-prefixed tag as semver, where `v2026` would be a
major version that needs a `/v2026` module-path suffix, and it would collide with the SDK's
`sdk/vX.Y.Z` tags. A tag without the `v` is invisible to Go's module tooling. The workflow
refuses a tag that isn't exactly this shape, so a stray tag cannot publish a release. The
first release, `v0.1.0`, predates the scheme; calendar versions sort after it, so Homebrew and
`install.sh` treat the first calendar release as an upgrade.

The editor's version says *when* it was released, not what it is compatible with. Plugin
compatibility is the SDK's semver; see [Releasing the plugin SDK](#releasing-the-plugin-sdk).

The workflow:

1. Builds the client for `darwin/arm64` and `darwin/amd64` on one macOS runner, and for
   `linux/amd64` and `linux/arm64` on native runners inside Alpine. The Linux binaries are
   statically linked against musl, so they run on any distribution.
2. Builds the static Linux container servers and every bundled plugin for all four
   platforms (no cgo needed, so all of it comes from one runner).
3. Packages everything (`scripts/release/package.sh`), runs `install.sh` against the
   packaged archives as a smoke test, and publishes a GitHub release with
   auto-generated notes.
4. Regenerates `Formula/indigo.rb` (`scripts/release/homebrew-formula.sh`) and pushes it to
   [`indiejames/homebrew-tap`](https://github.com/indiejames/homebrew-tap).

A tag containing a hyphen (`2026.10.0-rc.1`) is published as a **prerelease** and leaves the
Homebrew tap alone. `install.sh` also ignores it unless asked for with `--version`, since it
follows GitHub's "latest release", which excludes prereleases.

## Releasing the plugin SDK

The SDK (`sdk/`) is a separate Go module with its own semver, published by tagging
`sdk/vX.Y.Z`. That tag does not match the calendar-version trigger above, so it builds nothing — Go's
module proxy picks it up directly. When the SDK's public API (or `sdk/pluginproto/plugin.capnp`)
changes:

1. Bump `Version` in `sdk/version.go`. The editor checks every plugin's `sdk_version` against
   this constant, so it must match the tag exactly. While it is `0.x`, a minor bump means
   every plugin built against the previous minor is refused.
2. Bump `sdk_version` in the bundled plugins' `plugin.toml` files to match, or they are
   refused by the editor built in the same release.
3. Tag the same commit: `git tag sdk/v0.2.0 && git push origin sdk/v0.2.0`.

## What a release contains

| Asset | Contents |
|---|---|
| `indigo-<tag>-<os>-<arch>.tar.gz` | `indigo`, both `indigo-server-linux-*` container servers, LICENSE, notices, README |
| `indigo-plugins-<tag>.tar.gz` | `plugins/<name>/` for every bundled plugin: `plugin.toml` plus a binary per platform |
| `checksums.txt` | sha256 of each archive; `install.sh` refuses an archive that does not match |

The plugins archive carries all four platforms' binaries on purpose: a dev container's
server runs the Linux build of each plugin from the host's plugin directory.

## Dry runs

- **In CI:** Actions → Release → *Run workflow*. Everything is built and packaged as
  `0.0.0-dev.<sha>` and kept as the `release` workflow artifact; nothing is published.
- **Locally:** `make dist` builds this machine's client plus every platform's servers and
  plugins into `dist/release`. To try the installer against it:

  ```sh
  mkdir -p /tmp/dl && ln -sfn "$PWD/dist/release" "/tmp/dl/$(git describe --tags --always --dirty --match '[0-9]*' --match 'v[0-9]*')"
  INDIGO_RELEASE_BASE=file:///tmp/dl HOME=/tmp/indigo-home \
    sh install.sh --version "$(git describe --tags --always --dirty --match '[0-9]*' --match 'v[0-9]*')" --plugins
  ```

## One-time setup for Homebrew

1. Create a public repository named **`homebrew-tap`** under the same owner as this one.
   It can start empty; the workflow creates `Formula/indigo.rb`.
2. Create a fine-grained personal access token with **Contents: Read and write** on that
   repository only.
3. Add it to this repository as an Actions secret named **`HOMEBREW_TAP_TOKEN`**.

Without the secret the release still publishes; the Homebrew job just logs a warning and
skips.

## macOS signing and notarization

Every Mach-O in a release — both clients and the macOS build of each plugin — is signed
with a Developer ID (hardened runtime, secure timestamp) and notarized by Apple, in the
workflow's macOS job (`scripts/release/sign-macos.sh`). That is what lets a copy downloaded
through a browser, which macOS quarantines, open without a Gatekeeper warning. A bare
executable cannot have its ticket stapled (only apps, disk images and packages can), so
Gatekeeper looks it up online the first time a quarantined copy runs. `curl` and Homebrew
downloads are not quarantined and never ask.

A tagged release **fails** if signing or notarization is not configured, so an unsigned
build cannot ship by accident. A manual dry run only warns.

### One-time setup

You need the **Account Holder** role on the Apple Developer team to create a Developer ID
certificate.

1. **Create the certificate.** In [Certificates, IDs &
   Profiles](https://developer.apple.com/account/resources/certificates/list), add a
   certificate of type **Developer ID Application** (not "Apple Development" or "Apple
   Distribution": those are for development and the App Store, and notarization rejects
   them). Choose the G2 Sub-CA if asked. Create the signing request with Keychain Access
   (*Certificate Assistant → Request a Certificate From a Certificate Authority*, saved to
   disk), upload it, then download the certificate and double-click it to install it.
2. **Export it for CI.** In Keychain Access, find *Developer ID Application: <name>
   (<team>)* under *My Certificates*, expand it to make sure the private key is attached,
   and export both as a `.p12` with a strong password. Then:

   ```sh
   base64 -i DeveloperID.p12 | gh secret set MACOS_CERT_P12 -R indiejames/indigo
   gh secret set MACOS_CERT_PASSWORD -R indiejames/indigo   # prompts for the password
   ```

   Delete the `.p12` file afterwards.
3. **Create a notarization API key.** In [App Store Connect → Users and Access →
   Integrations → Team Keys](https://appstoreconnect.apple.com/access/integrations/api),
   generate a key with the **Developer** role. Download `AuthKey_<id>.p8` (it can only be
   downloaded once) and note the **Key ID** and the **Issuer ID** shown above the list:

   ```sh
   gh secret set NOTARY_KEY_ID -R indiejames/indigo      # the Key ID
   gh secret set NOTARY_ISSUER_ID -R indiejames/indigo   # the Issuer ID
   base64 -i AuthKey_<id>.p8 | gh secret set NOTARY_KEY_P8 -R indiejames/indigo
   ```

Then run the workflow by hand (Actions → Release → *Run workflow*). The *Sign and
notarize* step should end with `notarization <id>: Accepted`. If it reports `Invalid`, the
step prints Apple's log, which names the binary and the reason.

### Signing locally

The same script signs with an identity already in your keychain:

```sh
security find-identity -v -p codesigning     # find the Developer ID's SHA-1
MACOS_SIGN_IDENTITY=<sha1> scripts/release/sign-macos.sh ./indigo
```

Pass the SHA-1 rather than the name when the name matches more than one certificate;
`codesign` refuses an ambiguous name. Add the three `NOTARY_*` variables to notarize as
well.
