# Release inputs and security updates

`deploy/build-inputs.json` records Go/base-image manifest digests, a Debian/security
repository snapshot, and the exact Chromium package version. Dockerfile defaults
match these inputs, including Compose builds. PostgreSQL/Redis image digests are
shared by Compose and Testcontainers; `make check` rejects drift. Snapshot apt
repositories preserve package identities and signatures. Expired snapshot
metadata validity dates are allowed; TLS and apt signature checks remain enabled.

## Build and review a release

From a clean, reviewed checkout:

```sh
make check test vet build sqlc-check
make test-integration
make test-e2e
make release VERSION=v0.1.0 IMAGE_PREFIX=novelbot
make scan-release VERSION=v0.1.0
```

Release builds require an explicit tag and clean Git checkout. They create local
API/worker/migration images and `release-artifacts/<version>/` with an image
archive, checksums, binary hashes, commit, architecture, input manifest digests,
actual Chromium version, and installed worker package versions. Image config
digests are distinguished from registry manifest digests. Registry publishing,
signing, promotion, and deployment are separate operator actions; these commands
do not push or deploy.

`scripts/scan-release.py` installs a version-pinned Trivy binary verified against
its recorded official archive checksum. It produces CycloneDX SBOMs for all three
images and JSON vulnerability reports for images and Go dependencies, recording
all severities. Fixed HIGH/CRITICAL findings block the command and tagged release
CI job; unfixed findings still require review before promotion. Scan summaries,
SBOMs, and checksums are preserved even when that gate fails. A scanner/database
download failure also fails the gate; it never creates a false passing result.

On `v*` tags, Linux CI first requires both test jobs to pass, then uploads the
identified archive, manifest, SBOMs, and scan reports as workflow artifacts. Those
artifacts aid review; a failed gate's artifacts are not approved releases.

Pinned inputs and `go.sum` make dependencies identifiable and repeatable. Archive
compression headers are deterministic and Go builds exclude local VCS metadata.
This does not claim byte-identical OCI archives across architectures or independent
build timestamps/BuildKit versions. Compare recorded binary/package identities
and test the exact architecture you deploy. The current capacity baseline is ARM64;
Linux AMD64 CI passing does not establish AMD64 worker capacity.

## Update and promotion policy

- Review Chromium/Go/Debian/PostgreSQL/Redis advisories and dependency findings
  weekly. Investigate critical exploitable browser/security advisories within
  24 hours and prepare a replacement release before continuing promotion.
- Update the Go module/toolchain, image manifest digests, Debian snapshot, and
  exact Chromium package version deliberately. Keep Dockerfile defaults and
  build-inputs.json aligned; update both dependency constants and Compose when
  changing PostgreSQL/Redis. Review the seccomp profile when changing Chromium.
- Every proposed input update must pass unit/race, sqlc, integration, and sandboxed
  two-worker e2e checks. Re-run `make capacity` on supported deployment hardware
  before promoting a changed browser/runtime image. Attach that report and its
  image IDs, architecture, resource envelope, and workload to the release review;
  do not reuse the historical six-slot result as evidence for a new image.
- Review all scan results and residual findings, then promote the exact identified
  images through staging. Run the rollout/rollback qualification from later
  roadmap tasks before a production rollout. Preserve the last qualified images
  and evidence for rollback; a failed scan or browser test blocks promotion.

The workflows and local scripts are the automation; advisory review and capacity
qualification still need an assigned release operator. Automatic image updates
without qualification are intentionally absent.
