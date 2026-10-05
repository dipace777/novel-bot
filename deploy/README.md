# Container browser sandbox

`chromium-seccomp.json` is adapted from the Docker seccomp profile from Microsoft Playwright
v1.63.0 (`utils/docker/seccomp_profile.json`), distributed under Apache 2.0; see
`PLAYWRIGHT-LICENSE`. Source:
https://github.com/microsoft/playwright/blob/v1.63.0/utils/docker/seccomp_profile.json

The profile permits the namespace syscalls Chromium's sandbox needs. The local
change also permits `chroot` inside Chromium's sandbox: dropping container
capabilities otherwise removes the upstream conditional `chroot` rule. No host
SYS_CHROOT capability is added. Workers run as a non-root user with dropped container capabilities, no added SYS_ADMIN, and
no `--no-sandbox` flag. The Docker host must support unprivileged user namespaces.
Each worker has its own 512 MiB shared-memory mount and PID namespace, an init
process to reap children, and a configurable memory limit. The example 4 GiB limit
and two slots are development settings; use workload measurements to size workers.
