# Releases

Pushing a semantic-version tag such as `v0.1.0` starts the release workflow.
It builds Linux AMD64 and ARM64 images for Control, Edge Agent, OpenResty, and
Web, then publishes them to:

```text
ghcr.io/yeshimin/httpv-control
ghcr.io/yeshimin/httpv-edge-agent
ghcr.io/yeshimin/httpv-openresty
ghcr.io/yeshimin/httpv-web
```

BuildKit emits maximum-mode provenance and an SBOM for every multi-platform
image. The workflow keyless-signs each immutable digest with Cosign, adds a
GitHub artifact attestation, and creates generated release notes with
`compose.release.json` and `config/httpv.json` attached.

## Create a release

Run the full local checks, update documentation, and then:

```sh
git tag -s v0.1.0 -m "HTTPV v0.1.0"
git push origin v0.1.0
```

The workflow intentionally runs only for `vMAJOR.MINOR.PATCH` tags. It does not
accept an arbitrary manual branch release.

## Run published images

Download `compose.release.json` and `httpv.json` from the GitHub release, keep
them in the same directory, then run:

```sh
export HTTPV_VERSION=0.1.0
export HTTPV_ADMIN_TOKEN='replace-with-a-long-random-value'
docker compose -f compose.release.json up -d
```

Pin an exact version in production rather than `latest`.

## Verify

Verify a signed image digest against the GitHub Actions OIDC identity:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/yeshimin/httpv/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/yeshimin/httpv-control@sha256:REPLACE_ME
```

GitHub attestations can also be verified with the GitHub CLI against the source
repository. Always deploy by version or digest after verification.

