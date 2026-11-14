# Licences of the machine-runner image

The image ships the tools below as separate programs and as data files. The program
`machine-runner` starts each program as its own process and reads the data files; it links
none of them. Each tool enters the image only through `tools.lock.yaml`, which also states
the exact version, the release address and the SHA-256 that the build checks.

| Tool | Version | Licence | How it is used |
|---|---|---|---|
| ansible-core | 2.21.6 | GPL-3.0-or-later | separate process |
| ansible-runner | 2.4.3 | Apache-2.0 | separate process |
| trivy | 0.76.1 | Apache-2.0 | separate process |
| libopenscap33 | 1.4.2+dfsg-1 | LGPL-2.1-or-later | separate process (the library of `oscap`) |
| openscap-common | 1.4.2+dfsg-1 | LGPL-2.1-or-later | data (schema files of `oscap`) |
| openscap-scanner | 1.4.2+dfsg-1 | LGPL-2.1-or-later | separate process (`oscap`) |
| python3-openscap | 1.4.2+dfsg-1 | LGPL-2.1-or-later | separate process (used by `openscap-utils`) |
| openscap-utils | 1.4.2+dfsg-1 | LGPL-2.1-or-later | separate process (`oscap-chroot`) |
| scap-security-guide | 0.1.82 | BSD-3-Clause | data (the data streams under `/content/scap/0.1.82`) |

Where the licence texts are in the image:

- ansible-core and ansible-runner: the `.dist-info` folder of each package under
  `/usr/local/lib/python3.13/dist-packages/`.
- trivy: `/usr/share/doc/trivy/LICENSE`.
- the OpenSCAP packages: `/usr/share/doc/<package>/copyright`.
- scap-security-guide: `/usr/share/doc/scap-security-guide/LICENSE`.
- this file: `/usr/share/doc/machine-runner/LICENSES.md`.

Where the source is: the wheels of ansible-core and ansible-runner are their source. The
source of trivy and of scap-security-guide is the release named by the address in
`tools.lock.yaml`. The source of the OpenSCAP packages is the Debian source package
`openscap` 1.4.2+dfsg-1, in the same snapshot of the Debian archive as the packages.

The Debian base image and the Debian packages that the tools depend on keep their own
licence files under `/usr/share/doc/<package>/copyright`.
