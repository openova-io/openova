{{- /*
openova.envFromFiles (SEC-200) — the entrypoint shim of route 3 of the
injection design: an image that reads only plain variables gets them from the
files mounted at /var/run/secrets/openova, and then runs its own entrypoint.

Input: a dict
  map    variable name -> file name (required, non-empty)
  exec   the image's original entrypoint and arguments (list of strings,
         required, non-empty), each shell-quoted into the last line

Output: the container's `command` and `args`, to be placed with nindent:

  containers:
    - name: app
      {{- include "openova.envFromFiles" (dict "map" (dict "DB_PASSWORD" "db-password") "exec" (list "docker-entrypoint.sh" "postgres")) | nindent 6 }}

A mapped file that is missing ends the container with exit code 78 before the
program starts.
*/ -}}
{{- define "openova.envFromFiles" -}}
{{- $map := .map | default dict -}}
{{- $exec := .exec | default list -}}
{{- if not (kindIs "map" $map) }}{{ fail "openova.envFromFiles: map must be a map of variable name to file name" }}{{ end -}}
{{- if not $map }}{{ fail "openova.envFromFiles: map is empty" }}{{ end -}}
{{- if not (kindIs "slice" $exec) }}{{ fail "openova.envFromFiles: exec must be a list of strings" }}{{ end -}}
{{- if not $exec }}{{ fail "openova.envFromFiles: exec is empty: write the image's original entrypoint and arguments" }}{{ end -}}
{{- range $var, $file := $map -}}
{{- if not (regexMatch "^[A-Za-z_][A-Za-z0-9_]*$" $var) }}{{ fail (printf "openova.envFromFiles: %q is not a variable name" $var) }}{{ end -}}
{{- if not (regexMatch "^[a-z0-9][-a-z0-9._]*$" (toString $file)) }}{{ fail (printf "openova.envFromFiles: file %q of %s is not a mounted object name" (toString $file) $var) }}{{ end -}}
{{- end -}}
{{- $quoted := list -}}
{{- range $exec -}}
{{- $quoted = append $quoted (printf "'%s'" (replace "'" "'\\''" (toString .))) -}}
{{- end -}}
command: ["/bin/sh", "-c"]
args:
  - |
    set -eu
    while IFS='=' read -r var file; do
      [ -n "$var" ] || continue
      [ -r "/var/run/secrets/openova/$file" ] || { echo "openova: secret file $file is not mounted" >&2; exit 78; }
      export "$var=$(cat "/var/run/secrets/openova/$file")"
    done <<'OPENOVA_ENV_MAP'
{{- range $var, $file := $map }}
    {{ $var }}={{ $file }}
{{- end }}
    OPENOVA_ENV_MAP
    exec {{ join " " $quoted }}
{{- end -}}
