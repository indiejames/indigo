#!/bin/sh
# Sign macOS binaries with a Developer ID and notarize them.
#
#   scripts/release/sign-macos.sh <binary>...
#
# Signing identity, one of:
#   MACOS_SIGN_IDENTITY       a "Developer ID Application: ..." identity already in
#                             a keychain (local use) — its name, or its SHA-1 from
#                             `security find-identity -v -p codesigning`, which is
#                             unambiguous when the name matches several certificates
#   MACOS_CERT_P12            base64 of an exported .p12 holding that certificate
#   MACOS_CERT_PASSWORD       and its private key, imported into a throwaway
#                             keychain for the length of this script (CI)
#
# Notarization, with an App Store Connect API key (all three, or none to skip):
#   NOTARY_KEY_ID             the key's ID
#   NOTARY_ISSUER_ID          the issuer ID shown above the key list
#   NOTARY_KEY_P8             base64 of the downloaded AuthKey_<id>.p8
#
# With no identity at all the binaries are left as the Go linker made them
# (ad-hoc signed) and the script exits 0 — unless REQUIRE_SIGNING=1, which the
# release workflow sets for real releases so an unsigned build cannot ship by
# accident.
#
# A bare executable cannot have its notarization ticket stapled (only apps,
# disk images and installer packages can), so Gatekeeper looks the ticket up
# online the first time a quarantined copy runs. Downloads through curl or
# Homebrew are not quarantined and never ask.
set -eu

[ $# -gt 0 ] || { echo "usage: sign-macos.sh <binary>..." >&2; exit 1; }

identity=${MACOS_SIGN_IDENTITY:-}
work=$(mktemp -d)
keychain=""
cleanup() {
	if [ -n "$keychain" ]; then
		security delete-keychain "$keychain" >/dev/null 2>&1 || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

if [ -z "$identity" ] && [ -n "${MACOS_CERT_P12:-}" ]; then
	keychain="$work/signing.keychain-db"
	kcpass=$(openssl rand -hex 24)
	printf '%s' "$MACOS_CERT_P12" | base64 --decode > "$work/cert.p12"
	security create-keychain -p "$kcpass" "$keychain"
	security set-keychain-settings -lut 3600 "$keychain"
	security unlock-keychain -p "$kcpass" "$keychain"
	security import "$work/cert.p12" -k "$keychain" -P "${MACOS_CERT_PASSWORD:-}" \
		-T /usr/bin/codesign >/dev/null
	# Let codesign use the key without a GUI prompt.
	security set-key-partition-list -S apple-tool:,apple: -s -k "$kcpass" "$keychain" >/dev/null
	# codesign only searches keychains on the search list.
	# shellcheck disable=SC2046
	security list-keychains -d user -s "$keychain" $(security list-keychains -d user | tr -d '"')
	# By SHA-1 rather than name: a name can match several certificates.
	identity=$(security find-identity -v -p codesigning "$keychain" |
		awk '/"Developer ID Application: / { print $2; exit }')
	[ -n "$identity" ] || { echo "MACOS_CERT_P12 holds no Developer ID Application identity" >&2; exit 1; }
fi

if [ -z "$identity" ]; then
	if [ "${REQUIRE_SIGNING:-0}" = 1 ]; then
		echo "no signing identity (set MACOS_CERT_P12/MACOS_CERT_PASSWORD) and REQUIRE_SIGNING=1" >&2
		exit 1
	fi
	echo "::warning::no macOS signing identity configured; binaries are only ad-hoc signed"
	exit 0
fi

if printf '%s' "$identity" | grep -Eq '^[0-9A-Fa-f]{40}$'; then
	: # a SHA-1; codesign reports what it resolves to
elif [ "${identity#Developer ID Application:}" = "$identity" ]; then
	echo "warning: '$identity' is not a Developer ID Application identity; notarization will reject it" >&2
fi

for bin in "$@"; do
	echo "signing $bin"
	# Hardened runtime and a secure timestamp are both required for notarization.
	codesign --force --options runtime --timestamp --sign "$identity" "$bin"
	codesign --verify --strict --verbose=2 "$bin"
done

if [ -z "${NOTARY_KEY_ID:-}" ] || [ -z "${NOTARY_ISSUER_ID:-}" ] || [ -z "${NOTARY_KEY_P8:-}" ]; then
	if [ "${REQUIRE_SIGNING:-0}" = 1 ]; then
		echo "signed, but notarization credentials (NOTARY_KEY_ID/NOTARY_ISSUER_ID/NOTARY_KEY_P8) are missing" >&2
		exit 1
	fi
	echo "::warning::signed but not notarized (no NOTARY_* credentials)"
	exit 0
fi

printf '%s' "$NOTARY_KEY_P8" | base64 --decode > "$work/key.p8"
# notarytool takes an archive, not loose binaries. ditto keeps the signatures intact.
mkdir "$work/submit"
for bin in "$@"; do
	cp "$bin" "$work/submit/"
done
ditto -c -k "$work/submit" "$work/submit.zip"

echo "submitting $# binaries for notarization (this usually takes a few minutes)"
xcrun notarytool submit "$work/submit.zip" \
	--key "$work/key.p8" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID" \
	--wait --timeout 30m --output-format plist > "$work/result.plist"
status=$(/usr/libexec/PlistBuddy -c 'Print :status' "$work/result.plist" 2>/dev/null || echo unknown)
id=$(/usr/libexec/PlistBuddy -c 'Print :id' "$work/result.plist" 2>/dev/null || echo "")
echo "notarization $id: $status"
if [ "$status" != Accepted ]; then
	# The log names the binary and the reason, which the status alone does not.
	[ -n "$id" ] && xcrun notarytool log "$id" \
		--key "$work/key.p8" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID" || true
	exit 1
fi
